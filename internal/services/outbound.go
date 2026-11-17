package services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"neosell/internal/xui"
)

type clientRow struct {
	ID, LocRowID, TemplateID, LocationID int64
	Email, UUID                          string
	Created                              bool
	LastUp, LastDown                     int64
	LocQuota, LocExpires                 int64
	LocStatus, SubID                     string
}

func (m *Manager) clientsOf(ctx context.Context, serviceID int64) ([]clientRow, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT c.id, l.id, c.template_id, l.location_id, c.email, c.uuid, c.created, c.last_up, c.last_down,
		l.quota_bytes, l.expires_at, l.status, l.sub_id
		FROM service_location_clients c JOIN service_locations l ON l.id = c.service_location_id WHERE l.service_id = ? ORDER BY c.id`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []clientRow
	for rows.Next() {
		var c clientRow
		if err := rows.Scan(&c.ID, &c.LocRowID, &c.TemplateID, &c.LocationID, &c.Email, &c.UUID, &c.Created, &c.LastUp, &c.LastDown,
			&c.LocQuota, &c.LocExpires, &c.LocStatus, &c.SubID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ssPassword derives a stable Shadowsocks key of the right length.
func ssPassword(uuid string, templateID int64, method string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", uuid, templateID)))
	n := 32
	if strings.Contains(method, "aes-128") {
		n = 16
	}
	return base64.StdEncoding.EncodeToString(sum[:n])
}

func inboundMethod(in *xui.Inbound) string {
	var s struct {
		Method string `json:"method"`
	}
	json.Unmarshal([]byte(in.Settings), &s)
	return s.Method
}

// xclient builds the 3x-ui client for a row in its desired state.
func xclient(in *xui.Inbound, tpl *OutboundTemplate, c clientRow, enabled bool, maxIPs int) xui.XClient {
	x := xui.XClient{Email: c.Email, LimitIP: maxIPs, TotalGB: c.LocQuota, Enable: enabled, SubID: c.SubID, Comment: "neosell"}
	if c.LocExpires > 0 {
		x.ExpiryTime = c.LocExpires * 1000
	}
	switch in.Protocol {
	case "vless":
		x.ID, x.Flow = c.UUID, tpl.Flow
	case "vmess":
		x.ID = c.UUID
	case "trojan":
		x.Password = c.UUID
	case "shadowsocks":
		x.Password = ssPassword(c.UUID, c.TemplateID, inboundMethod(in))
	}
	return x
}

// syncOutbound makes every 3x-ui client of a service match its desired state.
// reset also zeroes the panel's traffic counters (renewal).
func (m *Manager) syncOutbound(ctx context.Context, serviceID int64, reset bool) error {
	s, err := m.service(ctx, serviceID)
	if err != nil {
		return nil // gone
	}
	clients, err := m.clientsOf(ctx, serviceID)
	if err != nil {
		return err
	}
	inbounds := map[int64]*xui.Inbound{}
	var errs []string
	allOK := true
	for _, c := range clients {
		tpl, err := m.outboundTemplate(ctx, c.TemplateID)
		if err != nil {
			continue
		}
		cl, err := m.XUI(ctx, tpl.NodeID)
		if err != nil {
			errs = append(errs, err.Error())
			allOK = false
			continue
		}
		in := inbounds[tpl.ID]
		if in == nil {
			if in, err = cl.Inbound(ctx, tpl.InboundID); err != nil {
				errs = append(errs, err.Error())
				allOK = false
				continue
			}
			inbounds[tpl.ID] = in
		}
		if s.Status == StDeleted || s.Status == StFailed {
			if c.Created {
				if err := cl.DelClient(ctx, tpl.InboundID, in.Protocol, xclient(in, tpl, c, false, 0)); err != nil {
					errs = append(errs, err.Error())
					continue
				}
				m.DB.ExecContext(ctx, `UPDATE service_location_clients SET created = 0 WHERE id = ?`, c.ID)
			}
			continue
		}
		enabled := (s.Status == StActive) && c.LocStatus == "active" || (s.Status == StProvisioning && c.LocStatus == "provisioning")
		x := xclient(in, tpl, c, enabled, s.MaxIPs)
		if !c.Created {
			err = cl.AddClient(ctx, tpl.InboundID, x)
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				err = cl.UpdateClient(ctx, tpl.InboundID, in.Protocol, x)
			}
			if err == nil {
				m.DB.ExecContext(ctx, `UPDATE service_location_clients SET created = 1, last_up = 0, last_down = 0 WHERE id = ?`, c.ID)
			}
		} else {
			err = cl.UpdateClient(ctx, tpl.InboundID, in.Protocol, x)
			if err == nil && reset {
				if err = cl.ResetClientTraffic(ctx, tpl.InboundID, c.Email); err == nil {
					m.DB.ExecContext(ctx, `UPDATE service_location_clients SET last_up = 0, last_down = 0 WHERE id = ?`, c.ID)
				}
			}
		}
		if err != nil {
			errs = append(errs, err.Error())
			allOK = false
		}
	}
	if s.Status == StProvisioning && allOK {
		m.DB.ExecContext(ctx, `UPDATE service_locations SET status = 'active' WHERE service_id = ? AND status = 'provisioning'`, serviceID)
		m.SetStatus(ctx, serviceID, StActive, "")
		m.notify(ctx, s.UserID, "service.ready", "سرویس آماده است", fmt.Sprintf("سرویس #%d (%s) ساخته شد. لینک ساب و کانفیگ‌ها در پنل است.", s.ID, s.Title))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// FailProvisioning is called when outbound provisioning keeps failing:
// refund and remove what was created.
func (m *Manager) FailProvisioning(ctx context.Context, serviceID int64, reason string) {
	s, err := m.service(ctx, serviceID)
	if err != nil || s.Status != StProvisioning {
		return
	}
	var orderID int64
	m.DB.QueryRowContext(ctx, `SELECT id FROM orders WHERE service_id = ? AND kind = 'new' ORDER BY id LIMIT 1`, serviceID).Scan(&orderID)
	m.DB.ExecContext(ctx, `UPDATE services SET status = 'failed', status_reason = ?, status_at = ? WHERE id = ?`, reason, m.now(), serviceID)
	m.DB.ExecContext(ctx, `UPDATE service_ports SET active = 0 WHERE service_id = ?`, serviceID)
	m.DB.ExecContext(ctx, `UPDATE products SET sold = MAX(sold - 1, 0) WHERE id = ?`, s.ProductID)
	if orderID > 0 {
		m.Refund(ctx, orderID, "بازگشت وجه: ساخت سرویس انجام نشد")
	}
	m.enqueueSync(ctx, serviceID, false) // removes any half-created clients (status failed → treated as delete below)
	m.notify(ctx, s.UserID, "service.failed", "ساخت سرویس انجام نشد", fmt.Sprintf("ساخت سرویس #%d انجام نشد و مبلغ به کیف پول برگشت. %s", serviceID, reason))
}

// ---------- subscription ----------

type SubContent struct {
	Links     []string
	Outbounds []map[string]any
	Up, Down  int64
	Total     int64
	Expire    int64
	Title     string
}

// Subscription renders a service's links, optionally for one location.
func (m *Manager) Subscription(ctx context.Context, token string, locationID int64) (*SubContent, error) {
	var sid int64
	if err := m.DB.QueryRowContext(ctx, `SELECT id FROM services WHERE sub_token = ? AND type = 'outbound' AND status NOT IN ('deleted','failed')`, token).Scan(&sid); err != nil {
		return nil, ErrNotFound
	}
	s, err := m.service(ctx, sid)
	if err != nil {
		return nil, err
	}
	clients, err := m.clientsOf(ctx, sid)
	if err != nil {
		return nil, err
	}
	out := &SubContent{Expire: s.ExpiresAt, Title: s.Title}
	seenLoc := map[int64]bool{}
	inbounds := map[int64]*xui.Inbound{}
	for _, c := range clients {
		if locationID > 0 && c.LocationID != locationID {
			continue
		}
		if !seenLoc[c.LocRowID] {
			seenLoc[c.LocRowID] = true
			var up, down int64
			m.DB.QueryRowContext(ctx, `SELECT used_up, used_down FROM service_locations WHERE id = ?`, c.LocRowID).Scan(&up, &down)
			out.Up += up
			out.Down += down
			out.Total += c.LocQuota
		}
		if !c.Created {
			continue
		}
		tpl, err := m.outboundTemplate(ctx, c.TemplateID)
		if err != nil {
			continue
		}
		in := inbounds[tpl.ID]
		if in == nil {
			cl, err := m.XUI(ctx, tpl.NodeID)
			if err != nil {
				continue
			}
			if in, err = cl.Inbound(ctx, tpl.InboundID); err != nil {
				continue
			}
			inbounds[tpl.ID] = in
		}
		var locName, flag string
		m.DB.QueryRowContext(ctx, `SELECT name, flag FROM locations WHERE id = ?`, c.LocationID).Scan(&locName, &flag)
		tag := strings.TrimSpace(fmt.Sprintf("%s %s %s", flag, locName, tpl.Label))
		if tpl.Label == "" {
			tag = strings.TrimSpace(flag + " " + locName + " " + tpl.Tag)
		}
		ep := xui.Endpoint{Host: tpl.PublicHost, Port: tpl.PublicPort, Tag: tag}
		x := xclient(in, tpl, c, true, 0)
		if link, err := xui.ShareLink(in, x, ep); err == nil {
			out.Links = append(out.Links, link)
		}
		ep.Tag = fmt.Sprintf("ns-%d-%s", c.LocationID, strings.ToLower(strings.ReplaceAll(nz(tpl.Tag, "out"), " ", "-")))
		if ob, err := xui.Outbound(in, x, ep); err == nil {
			out.Outbounds = append(out.Outbounds, ob)
		}
	}
	if locationID > 0 && len(seenLoc) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}
