package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"neosell/internal/agentproto"
)

// addUsage records a delta for a service (and the hourly series).
func (m *Manager) addUsage(ctx context.Context, tx *sql.Tx, serviceID, up, down int64) error {
	if up <= 0 && down <= 0 {
		return nil
	}
	if up < 0 {
		up = 0
	}
	if down < 0 {
		down = 0
	}
	if _, err := tx.ExecContext(ctx, `UPDATE services SET used_up = used_up + ?, used_down = used_down + ? WHERE id = ?`, up, down, serviceID); err != nil {
		return err
	}
	hour := m.now() / 3600 * 3600
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_hourly(service_id, hour, up, down) VALUES (?, ?, ?, ?)
		ON CONFLICT(service_id, hour) DO UPDATE SET up = up + excluded.up, down = down + excluded.down`, serviceID, hour, up, down)
	return err
}

// ApplyAgentUsage turns an agent's absolute counters into deltas. A total
// lower than the last one means the agent lost its state: the new total is
// all new traffic.
func (m *Manager) ApplyAgentUsage(ctx context.Context, nodeID int64, u agentproto.Usage) error {
	src := "node:" + strconv.FormatInt(nodeID, 10)
	var touched []int64
	err := m.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, c := range u.Counters {
			var lastUp, lastDown int64
			err := tx.QueryRowContext(ctx, `SELECT up, down FROM usage_counters WHERE source = ? AND service_id = ?`, src, c.ServiceID).Scan(&lastUp, &lastDown)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			du, dd := c.Up-lastUp, c.Down-lastDown
			if du < 0 || dd < 0 { // the agent lost its totals: everything reported is new
				du, dd = c.Up, c.Down
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage_counters(source, service_id, up, down, updated_at) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(source, service_id) DO UPDATE SET up = excluded.up, down = excluded.down, updated_at = excluded.updated_at`,
				src, c.ServiceID, c.Up, c.Down, m.now()); err != nil {
				return err
			}
			var owner int64
			if tx.QueryRowContext(ctx, `SELECT COALESCE(node_id,0) FROM services WHERE id = ?`, c.ServiceID).Scan(&owner) != nil || owner != nodeID {
				continue // stale counter for a moved/deleted service
			}
			if du > 0 || dd > 0 {
				if err := m.addUsage(ctx, tx, c.ServiceID, du, dd); err != nil {
					return err
				}
				touched = append(touched, c.ServiceID)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range touched {
		m.afterUsage(ctx, id)
	}
	return nil
}

// afterUsage checks quota and alert thresholds of a port service.
func (m *Manager) afterUsage(ctx context.Context, id int64) {
	s, err := m.service(ctx, id)
	if err != nil {
		return
	}
	used := s.UsedUp + s.UsedDown
	if s.Type == "port" && s.QuotaBytes > 0 && used >= s.QuotaBytes && s.Status == StActive {
		m.SetStatus(ctx, id, StLimited, "حجم تمام شد")
		return
	}
	if s.QuotaBytes > 0 && s.Status == StActive {
		m.usageAlerts(ctx, s, used, s.QuotaBytes, "")
	}
}

func (m *Manager) usageAlerts(ctx context.Context, s *svcRow, used, quota int64, scope string) {
	var notified map[string]bool
	json.Unmarshal([]byte(s.notifiedRaw), &notified)
	if notified == nil {
		notified = map[string]bool{}
	}
	pct := used * 100 / quota
	changed := false
	for _, t := range strings.Split(m.Settings.String(ctx, "notify.usage_thresholds"), ",") {
		th, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil || th <= 0 || th >= 100 {
			continue
		}
		key := fmt.Sprintf("u%s%d", scope, th)
		if pct >= int64(th) && !notified[key] {
			notified[key] = true
			changed = true
			where := ""
			if scope != "" {
				where = " (لوکیشن " + scope + ")"
			}
			m.notify(ctx, s.UserID, "usage.threshold", fmt.Sprintf("%d٪ حجم مصرف شد", th),
				fmt.Sprintf("%d٪ حجم سرویس #%d (%s)%s مصرف شده است.", th, s.ID, s.Title, where))
		}
	}
	if changed {
		raw, _ := json.Marshal(notified)
		m.DB.ExecContext(ctx, `UPDATE services SET notified = ? WHERE id = ?`, string(raw), s.ID)
	}
}

// ApplyAgentEvents reacts to quota/expiry cuts the agent made on its own.
func (m *Manager) ApplyAgentEvents(ctx context.Context, nodeID int64, evs []agentproto.Event) {
	for _, e := range evs {
		s, err := m.service(ctx, e.ServiceID)
		if err != nil || s.NodeID != nodeID || s.Status != StActive {
			continue
		}
		switch e.Kind {
		case "quota":
			m.SetStatus(ctx, s.ID, StLimited, "حجم تمام شد")
		case "expired":
			m.SetStatus(ctx, s.ID, StExpired, "مدت تمام شد")
		}
	}
}

// PollXUI reads traffic of every client on every 3x-ui panel (one request per
// panel), adds the deltas to locations and services, and enforces each
// location's own quota.
func (m *Manager) PollXUI(ctx context.Context) {
	nodes, err := m.Nodes(ctx, "xui")
	if err != nil {
		return
	}
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		cl, err := m.XUI(ctx, n.ID)
		if err != nil {
			continue
		}
		stats, err := cl.Stats(ctx)
		if err != nil {
			m.SetNodeStatus(ctx, n.ID, "error", err.Error(), nil)
			slog.Warn("xui poll failed", "node", n.Name, "err", err)
			continue
		}
		m.SetNodeStatus(ctx, n.ID, "online", "", nil)
		conv := make(map[string]xstat, len(stats))
		for k, v := range stats {
			conv[k] = xstat{Up: v.Up, Down: v.Down}
		}
		m.ApplyClientStats(ctx, n.ID, conv)
	}
}

type xstat = struct{ Up, Down int64 }

// ApplyClientStats is the testable core of PollXUI.
func (m *Manager) ApplyClientStats(ctx context.Context, nodeID int64, stats map[string]xstat) {
	rows, err := m.DB.QueryContext(ctx, `SELECT c.id, c.email, c.last_up, c.last_down, l.id, l.service_id
		FROM service_location_clients c JOIN outbound_templates t ON t.id = c.template_id
		JOIN service_locations l ON l.id = c.service_location_id JOIN services s ON s.id = l.service_id
		WHERE t.node_id = ? AND c.created = 1 AND s.status NOT IN ('deleted','failed')`, nodeID)
	if err != nil {
		return
	}
	type upd struct {
		cid, lid, sid    int64
		cur              xstat
		lastUp, lastDown int64
	}
	var list []upd
	for rows.Next() {
		var u upd
		var email string
		rows.Scan(&u.cid, &email, &u.lastUp, &u.lastDown, &u.lid, &u.sid)
		st, ok := stats[email]
		if !ok {
			continue
		}
		u.cur = st
		list = append(list, u)
	}
	rows.Close()
	locs := map[int64]int64{} // location row → service
	err = m.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, u := range list {
			du, dd := u.cur.Up-u.lastUp, u.cur.Down-u.lastDown
			if du < 0 || dd < 0 { // counters were reset on the panel
				du, dd = u.cur.Up, u.cur.Down
			}
			if _, err := tx.ExecContext(ctx, `UPDATE service_location_clients SET last_up = ?, last_down = ? WHERE id = ?`, u.cur.Up, u.cur.Down, u.cid); err != nil {
				return err
			}
			if du == 0 && dd == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE service_locations SET used_up = used_up + ?, used_down = used_down + ? WHERE id = ?`, du, dd, u.lid); err != nil {
				return err
			}
			if err := m.addUsage(ctx, tx, u.sid, du, dd); err != nil {
				return err
			}
			locs[u.lid] = u.sid
		}
		return nil
	})
	if err != nil {
		slog.Error("xui usage", "err", err)
		return
	}
	for lid, sid := range locs {
		m.afterLocationUsage(ctx, lid, sid)
	}
}

func (m *Manager) afterLocationUsage(ctx context.Context, lid, sid int64) {
	var quota, up, down, locID int64
	var status, name string
	if m.DB.QueryRowContext(ctx, `SELECT l.quota_bytes, l.used_up, l.used_down, l.status, l.location_id, COALESCE(lc.name,'')
		FROM service_locations l LEFT JOIN locations lc ON lc.id = l.location_id WHERE l.id = ?`, lid).Scan(&quota, &up, &down, &status, &locID, &name) != nil {
		return
	}
	s, err := m.service(ctx, sid)
	if err != nil || quota == 0 {
		return
	}
	if up+down >= quota && status == "active" {
		m.DB.ExecContext(ctx, `UPDATE service_locations SET status = 'limited' WHERE id = ?`, lid)
		m.notify(ctx, s.UserID, "location.limited", "حجم یک لوکیشن تمام شد",
			fmt.Sprintf("حجم لوکیشن %s در سرویس #%d تمام شد. برای ادامه حجم اضافه بخرید.", name, sid))
		var active int
		m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM service_locations WHERE service_id = ? AND status = 'active'`, sid).Scan(&active)
		if active == 0 && s.Status == StActive {
			m.SetStatus(ctx, sid, StLimited, "حجم همه‌ی لوکیشن‌ها تمام شد")
		} else {
			m.enqueueSync(ctx, sid, false)
		}
		return
	}
	if s.Status == StActive {
		m.usageAlerts(ctx, s, up+down, quota, name)
	}
}
