package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"neosell/internal/pricing"
)

type svcRow struct {
	ID, UserID, ProductID                 int64
	Type, Model, Title, Status, Reason    string
	QuotaBytes, UsedUp, UsedDown          int64
	PeriodStart, ExpiresAt                int64
	RateMbit, MaxIPs                      int
	NodeID                                int64
	Core, Delivery, Proto, SubToken, UUID string
	selectionRaw, specRaw, notifiedRaw    string
	StatusAt, CreatedAt                   int64
	PaygGB, PaygDay, PaygBilled, PaygAt   int64
}

const svcCols = `id, user_id, COALESCE(product_id,0), type, model, title, status, status_reason, quota_bytes, used_up, used_down,
	period_start, expires_at, rate_mbit, max_ips, COALESCE(node_id,0), core, delivery, proto, sub_token, xray_uuid, selection, spec,
	notified, status_at, created_at, payg_per_gb, payg_per_day, payg_billed_bytes, payg_billed_at`

func scanSvc(row interface{ Scan(...any) error }) (*svcRow, error) {
	s := &svcRow{}
	err := row.Scan(&s.ID, &s.UserID, &s.ProductID, &s.Type, &s.Model, &s.Title, &s.Status, &s.Reason, &s.QuotaBytes, &s.UsedUp, &s.UsedDown,
		&s.PeriodStart, &s.ExpiresAt, &s.RateMbit, &s.MaxIPs, &s.NodeID, &s.Core, &s.Delivery, &s.Proto, &s.SubToken, &s.UUID,
		&s.selectionRaw, &s.specRaw, &s.notifiedRaw, &s.StatusAt, &s.CreatedAt, &s.PaygGB, &s.PaygDay, &s.PaygBilled, &s.PaygAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

func (m *Manager) service(ctx context.Context, id int64) (*svcRow, error) {
	return scanSvc(m.DB.QueryRowContext(ctx, `SELECT `+svcCols+` FROM services WHERE id = ?`, id))
}

func (m *Manager) serviceTx(ctx context.Context, tx *sql.Tx, id int64) (*svcRow, error) {
	return scanSvc(tx.QueryRowContext(ctx, `SELECT `+svcCols+` FROM services WHERE id = ?`, id))
}

// ---------- views ----------

type PortInfo struct {
	Port   int    `json:"port"`
	ToPort int    `json:"to_port"`
	Host   string `json:"host"`
}

type TunnelInfo struct {
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	Core       string `json:"core"`
	BridgePort int    `json:"bridge_port,omitempty"`
	PeerStatus string `json:"peer_status"`
	InstallCmd string `json:"install_cmd,omitempty"`
	PeerSetup  string `json:"peer_setup,omitempty"`
}

type LocationInfo struct {
	ID         int64    `json:"id"`
	LocationID int64    `json:"location_id"`
	Name       string   `json:"name"`
	Flag       string   `json:"flag"`
	QuotaBytes int64    `json:"quota_bytes"`
	UsedUp     int64    `json:"used_up"`
	UsedDown   int64    `json:"used_down"`
	ExpiresAt  int64    `json:"expires_at"`
	Status     string   `json:"status"`
	SubURL     string   `json:"sub_url"`
	Links      []string `json:"links,omitempty"`
}

type ServiceInfo struct {
	ID           int64             `json:"id"`
	UserID       int64             `json:"user_id"`
	UserEmail    string            `json:"user_email,omitempty"`
	ProductID    int64             `json:"product_id"`
	Type         string            `json:"type"`
	Model        string            `json:"model"`
	Title        string            `json:"title"`
	Status       string            `json:"status"`
	StatusReason string            `json:"status_reason,omitempty"`
	QuotaBytes   int64             `json:"quota_bytes"`
	UsedUp       int64             `json:"used_up"`
	UsedDown     int64             `json:"used_down"`
	PeriodStart  int64             `json:"period_start"`
	ExpiresAt    int64             `json:"expires_at"`
	RateMbit     int               `json:"rate_mbit"`
	MaxIPs       int               `json:"max_ips"`
	NodeID       int64             `json:"node_id,omitempty"`
	NodeName     string            `json:"node_name,omitempty"`
	Core         string            `json:"core,omitempty"`
	Delivery     string            `json:"delivery,omitempty"`
	Proto        string            `json:"proto,omitempty"`
	Spec         pricing.Spec      `json:"spec"`
	Selection    pricing.Selection `json:"selection"`
	Ports        []PortInfo        `json:"ports,omitempty"`
	Tunnel       *TunnelInfo       `json:"tunnel,omitempty"`
	Locations    []LocationInfo    `json:"locations,omitempty"`
	SubURL       string            `json:"sub_url,omitempty"`
	PaygPerGB    int64             `json:"payg_per_gb,omitempty"`
	PaygPerDay   int64             `json:"payg_per_day,omitempty"`
	CreatedAt    int64             `json:"created_at"`
}

func (m *Manager) base() string { return strings.TrimRight(m.PublicURL, "/") }

func (m *Manager) info(ctx context.Context, s *svcRow, detail bool) ServiceInfo {
	v := ServiceInfo{ID: s.ID, UserID: s.UserID, ProductID: s.ProductID, Type: s.Type, Model: s.Model, Title: s.Title, Status: s.Status,
		StatusReason: s.Reason, QuotaBytes: s.QuotaBytes, UsedUp: s.UsedUp, UsedDown: s.UsedDown, PeriodStart: s.PeriodStart,
		ExpiresAt: s.ExpiresAt, RateMbit: s.RateMbit, MaxIPs: s.MaxIPs, NodeID: s.NodeID, Core: s.Core, Delivery: s.Delivery,
		Proto: s.Proto, PaygPerGB: s.PaygGB, PaygPerDay: s.PaygDay, CreatedAt: s.CreatedAt}
	json.Unmarshal([]byte(s.specRaw), &v.Spec)
	json.Unmarshal([]byte(s.selectionRaw), &v.Selection)
	if s.Type == "port" {
		var host string
		m.DB.QueryRowContext(ctx, `SELECT name, public_host FROM nodes WHERE id = ?`, s.NodeID).Scan(&v.NodeName, &host)
		rows, _ := m.DB.QueryContext(ctx, `SELECT port, to_port FROM service_ports WHERE service_id = ? AND active = 1 ORDER BY port`, s.ID)
		if rows != nil {
			for rows.Next() {
				p := PortInfo{Host: host}
				rows.Scan(&p.Port, &p.ToPort)
				v.Ports = append(v.Ports, p)
			}
			rows.Close()
		}
		if s.Delivery == "dedicated" {
			t := &TunnelInfo{}
			var tok string
			if m.DB.QueryRowContext(ctx, `SELECT name, mode, core, bridge_port, peer_status, COALESCE(peer_token,''), peer_setup FROM tunnels WHERE service_id = ?`, s.ID).
				Scan(&t.Name, &t.Mode, &t.Core, &t.BridgePort, &t.PeerStatus, &tok, &t.PeerSetup) == nil {
				if detail && tok != "" && liveStatus(s.Status) {
					t.InstallCmd = fmt.Sprintf("bash <(curl -fsSL %s/i/%s)", m.base(), tok)
				}
				if !detail {
					t.PeerSetup = ""
				}
				v.Tunnel = t
			}
		}
	}
	if s.Type == "outbound" {
		v.SubURL = m.base() + "/sub/" + s.SubToken
		rows, _ := m.DB.QueryContext(ctx, `SELECT l.id, l.location_id, COALESCE(lc.name,''), COALESCE(lc.flag,''), l.quota_bytes, l.used_up, l.used_down, l.expires_at, l.status
			FROM service_locations l LEFT JOIN locations lc ON lc.id = l.location_id WHERE l.service_id = ? ORDER BY lc.sort, l.id`, s.ID)
		if rows != nil {
			for rows.Next() {
				var l LocationInfo
				rows.Scan(&l.ID, &l.LocationID, &l.Name, &l.Flag, &l.QuotaBytes, &l.UsedUp, &l.UsedDown, &l.ExpiresAt, &l.Status)
				l.SubURL = fmt.Sprintf("%s/sub/%s/%d", m.base(), s.SubToken, l.LocationID)
				v.Locations = append(v.Locations, l)
			}
			rows.Close()
		}
	}
	return v
}

// ServiceFor returns a service owned by userID (any user when userID = 0).
func (m *Manager) ServiceFor(ctx context.Context, id, userID int64) (*ServiceInfo, error) {
	s, err := m.service(ctx, id)
	if err != nil {
		return nil, err
	}
	if userID > 0 && s.UserID != userID || userID > 0 && s.Status == StDeleted {
		return nil, ErrNotFound
	}
	v := m.info(ctx, s, true)
	m.DB.QueryRowContext(ctx, `SELECT COALESCE(email,'') FROM users WHERE id = ?`, s.UserID).Scan(&v.UserEmail)
	return &v, nil
}

type ListFilter struct {
	UserID   int64
	Status   string
	Type     string
	NodeID   int64
	Query    string
	BeforeID int64
	Limit    int
}

func (m *Manager) Services(ctx context.Context, f ListFilter) ([]ServiceInfo, error) {
	q := `SELECT ` + svcCols + ` FROM services WHERE 1=1`
	args := []any{}
	if f.UserID > 0 {
		q += ` AND user_id = ? AND status != 'deleted'`
		args = append(args, f.UserID)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Type != "" {
		q += ` AND type = ?`
		args = append(args, f.Type)
	}
	if f.NodeID > 0 {
		q += ` AND node_id = ?`
		args = append(args, f.NodeID)
	}
	if f.Query != "" {
		q += ` AND (CAST(id AS TEXT) = ? OR user_id IN (SELECT id FROM users WHERE email LIKE ?) OR id IN (SELECT service_id FROM service_ports WHERE CAST(port AS TEXT) = ?))`
		args = append(args, f.Query, "%"+f.Query+"%", f.Query)
	}
	if f.BeforeID > 0 {
		q += ` AND id < ?`
		args = append(args, f.BeforeID)
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	rows, err := m.DB.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	var list []*svcRow
	for rows.Next() {
		s, err := scanSvc(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, s)
	}
	rows.Close()
	out := make([]ServiceInfo, 0, len(list))
	for _, s := range list {
		v := m.info(ctx, s, false)
		if f.UserID == 0 {
			m.DB.QueryRowContext(ctx, `SELECT COALESCE(email,'') FROM users WHERE id = ?`, s.UserID).Scan(&v.UserEmail)
		}
		out = append(out, v)
	}
	return out, nil
}

// ---------- status changes ----------

// SetStatus moves a service to a new status and propagates it (agent / 3x-ui).
func (m *Manager) SetStatus(ctx context.Context, id int64, status, reason string) error {
	s, err := m.service(ctx, id)
	if err != nil {
		return err
	}
	if s.Status == status || s.Status == StDeleted {
		return nil
	}
	now := m.now()
	if _, err := m.DB.ExecContext(ctx, `UPDATE services SET status = ?, status_reason = ?, status_at = ?, updated_at = ? WHERE id = ?`, status, reason, now, now, id); err != nil {
		return err
	}
	if s.Type == "outbound" {
		if status == StExpired {
			m.DB.ExecContext(ctx, `UPDATE service_locations SET status = 'expired' WHERE service_id = ? AND status IN ('active','limited')`, id)
		}
		if status == StActive {
			m.DB.ExecContext(ctx, `UPDATE service_locations SET status = 'active' WHERE service_id = ? AND status = 'expired'`, id)
		}
		m.enqueueSync(ctx, id, false)
	} else {
		m.push(s.NodeID)
	}
	switch status {
	case StLimited:
		m.notify(ctx, s.UserID, "service.limited", "حجم سرویس تمام شد", fmt.Sprintf("حجم سرویس #%d (%s) تمام شد و متوقف شد. برای ادامه حجم اضافه بخرید یا تمدید کنید.", id, s.Title))
	case StExpired:
		m.notify(ctx, s.UserID, "service.expired", "سرویس منقضی شد", fmt.Sprintf("مدت سرویس #%d (%s) تمام شد. برای ادامه تمدید کنید.", id, s.Title))
	case StSuspended:
		m.notify(ctx, s.UserID, "service.suspended", "سرویس معلق شد", fmt.Sprintf("سرویس #%d (%s) معلق شد و پس از مدتی حذف می‌شود؛ برای حفظ آن تمدید کنید.", id, s.Title))
	case StActive:
		if s.Status == StWaitingPeer {
			m.notify(ctx, s.UserID, "service.active", "سرویس فعال شد", fmt.Sprintf("سرور خارج شما وصل شد و سرویس #%d فعال است.", id))
		}
	}
	return nil
}

// Delete removes a service everywhere and frees its ports.
func (m *Manager) Delete(ctx context.Context, id int64, reason string) error {
	s, err := m.service(ctx, id)
	if err != nil {
		return err
	}
	now := m.now()
	err = m.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'deleted', status_reason = ?, status_at = ?, updated_at = ? WHERE id = ?`, reason, now, now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE service_ports SET active = 0 WHERE service_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM tunnels WHERE service_id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	if s.Type == "outbound" {
		m.enqueueSync(ctx, id, false)
	} else {
		m.push(s.NodeID)
	}
	return nil
}

// AdminUpdate lets an admin change any limit of a service directly.
type AdminChange struct {
	QuotaBytes *int64  `json:"quota_bytes"`
	ExpiresAt  *int64  `json:"expires_at"`
	RateMbit   *int    `json:"rate_mbit"`
	MaxIPs     *int    `json:"max_ips"`
	ResetUsage bool    `json:"reset_usage"`
	Status     *string `json:"status"` // active | paused
}

func (m *Manager) AdminUpdate(ctx context.Context, id int64, c AdminChange) error {
	s, err := m.service(ctx, id)
	if err != nil {
		return err
	}
	if s.Status == StDeleted {
		return errors.New("سرویس حذف شده است")
	}
	sets, args := []string{"updated_at = ?"}, []any{m.now()}
	if c.QuotaBytes != nil && *c.QuotaBytes >= 0 {
		sets, args = append(sets, "quota_bytes = ?"), append(args, *c.QuotaBytes)
	}
	if c.ExpiresAt != nil && *c.ExpiresAt >= 0 {
		sets, args = append(sets, "expires_at = ?"), append(args, *c.ExpiresAt)
	}
	if c.RateMbit != nil && *c.RateMbit >= 0 {
		sets, args = append(sets, "rate_mbit = ?"), append(args, *c.RateMbit)
	}
	if c.MaxIPs != nil && *c.MaxIPs >= 0 {
		sets, args = append(sets, "max_ips = ?"), append(args, *c.MaxIPs)
	}
	if c.ResetUsage {
		sets = append(sets, "used_up = 0", "used_down = 0", "period_start = ?", "notified = '{}'")
		args = append(args, m.now())
	}
	if _, err := m.DB.ExecContext(ctx, `UPDATE services SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
		return err
	}
	if s.Type == "outbound" {
		if c.ExpiresAt != nil {
			m.DB.ExecContext(ctx, `UPDATE service_locations SET expires_at = ? WHERE service_id = ?`, *c.ExpiresAt, id)
		}
		if c.ResetUsage {
			m.DB.ExecContext(ctx, `UPDATE service_locations SET used_up = 0, used_down = 0, status = CASE WHEN status = 'limited' THEN 'active' ELSE status END WHERE service_id = ?`, id)
		}
	}
	// Re-evaluate the status against the new limits.
	s2, _ := m.service(ctx, id)
	next := s2.Status
	switch {
	case c.Status != nil && *c.Status == StPaused:
		next = StPaused
	case c.Status != nil && *c.Status == StActive || s2.Status == StLimited || s2.Status == StExpired || s2.Status == StSuspended:
		next = StActive
		if s2.ExpiresAt > 0 && s2.ExpiresAt <= m.now() {
			next = StExpired
		} else if s2.Type == "port" && s2.QuotaBytes > 0 && s2.UsedUp+s2.UsedDown >= s2.QuotaBytes {
			next = StLimited
		}
	}
	if next != s2.Status {
		return m.SetStatus(ctx, id, next, "تغییر توسط ادمین")
	}
	if s.Type == "outbound" {
		m.enqueueSync(ctx, id, c.ResetUsage)
	} else {
		m.push(s.NodeID)
	}
	return nil
}

// RotateSub issues a new subscription token (old links stop working).
func (m *Manager) RotateSub(ctx context.Context, id, userID int64) (string, error) {
	tok := randID(24)
	res, err := m.DB.ExecContext(ctx, `UPDATE services SET sub_token = ? WHERE id = ? AND user_id = ? AND type = 'outbound'`, tok, id, userID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return m.base() + "/sub/" + tok, nil
}

// UsageSeries returns hourly usage for charts (last `hours` hours).
type UsagePoint struct {
	At   int64 `json:"at"`
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

func (m *Manager) UsageSeries(ctx context.Context, id int64, hours int, daily bool) ([]UsagePoint, error) {
	since := m.now() - int64(hours)*3600
	q := `SELECT hour, up, down FROM usage_hourly WHERE service_id = ? AND hour >= ? ORDER BY hour`
	if daily {
		q = `SELECT (hour / 86400) * 86400, SUM(up), SUM(down) FROM usage_hourly WHERE service_id = ? AND hour >= ? GROUP BY hour / 86400 ORDER BY 1`
	}
	rows, err := m.DB.QueryContext(ctx, q, id, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsagePoint{}
	for rows.Next() {
		var p UsagePoint
		rows.Scan(&p.At, &p.Up, &p.Down)
		out = append(out, p)
	}
	return out, rows.Err()
}
