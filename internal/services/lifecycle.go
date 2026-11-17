package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"neosell/internal/wallet"
)

// Loop runs periodic work until ctx ends: 3x-ui usage (every minute) and
// lifecycle checks (expiry, suspension, deletion, alerts, billing).
func (m *Manager) Loop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		m.PollXUI(ctx)
		m.Lifecycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Manager) ids(ctx context.Context, q string, args ...any) []int64 {
	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		slog.Error("lifecycle query", "err", err)
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// Lifecycle applies time-based transitions. Safe to run any number of times.
func (m *Manager) Lifecycle(ctx context.Context) {
	now := m.now()
	day := int64(86400)

	// Expiry (the agent already cut port services at the exact second).
	for _, id := range m.ids(ctx, `SELECT id FROM services WHERE status IN ('active','limited') AND expires_at > 0 AND expires_at <= ?`, now) {
		m.SetStatus(ctx, id, StExpired, "مدت تمام شد")
	}
	// Expiry alerts.
	for _, d := range strings.Split(m.Settings.String(ctx, "notify.expiry_days"), ",") {
		n, err := strconv.Atoi(strings.TrimSpace(d))
		if err != nil || n <= 0 {
			continue
		}
		key := "e" + strconv.Itoa(n)
		for _, id := range m.ids(ctx, `SELECT id FROM services WHERE status = 'active' AND expires_at > ? AND expires_at <= ? AND json_extract(notified, '$.`+key+`') IS NULL`, now, now+int64(n)*day) {
			s, err := m.service(ctx, id)
			if err != nil {
				continue
			}
			left := (s.ExpiresAt - now + day - 1) / day
			m.notify(ctx, s.UserID, "service.expiring", "سرویس رو به اتمام است",
				fmt.Sprintf("سرویس #%d (%s) تا %d روز دیگر منقضی می‌شود. برای جلوگیری از قطعی تمدید کنید.", s.ID, s.Title, left))
			m.DB.ExecContext(ctx, `UPDATE services SET notified = json_set(notified, '$.`+key+`', 1) WHERE id = ?`, id)
		}
	}
	// Suspend, then delete, after the admin's grace periods.
	suspendAfter := m.Settings.Int(ctx, "lifecycle.suspend_after_days") * day
	deleteAfter := m.Settings.Int(ctx, "lifecycle.delete_after_days") * day
	for _, id := range m.ids(ctx, `SELECT id FROM services WHERE status IN ('limited','expired') AND status_at <= ?`, now-suspendAfter) {
		m.SetStatus(ctx, id, StSuspended, "مهلت تمدید تمام شد")
	}
	for _, id := range m.ids(ctx, `SELECT id FROM services WHERE status = 'suspended' AND status_at <= ?`, now-deleteAfter) {
		m.Delete(ctx, id, "حذف خودکار پس از تعلیق")
	}
	// Dedicated tunnels whose customer never connected: cancel and refund.
	peerWait := m.Settings.Int(ctx, "lifecycle.peer_wait_hours") * 3600
	for _, id := range m.ids(ctx, `SELECT id FROM services WHERE status = 'waiting_peer' AND created_at <= ?`, now-peerWait) {
		m.cancelWaiting(ctx, id)
	}
	// Outbound services stuck in provisioning for an hour: refund.
	for _, id := range m.ids(ctx, `SELECT id FROM services WHERE type = 'outbound' AND status = 'provisioning' AND created_at <= ?`, now-3600) {
		m.FailProvisioning(ctx, id, "اتصال به سرور خارج برقرار نشد")
	}
	m.billPayg(ctx)
}

func (m *Manager) cancelWaiting(ctx context.Context, id int64) {
	s, err := m.service(ctx, id)
	if err != nil || s.Status != StWaitingPeer {
		return
	}
	var orderID int64
	m.DB.QueryRowContext(ctx, `SELECT id FROM orders WHERE service_id = ? AND kind = 'new' ORDER BY id LIMIT 1`, id).Scan(&orderID)
	m.Delete(ctx, id, "سرور خارج در مهلت وصل نشد")
	m.DB.ExecContext(ctx, `UPDATE services SET status = 'failed' WHERE id = ?`, id)
	m.DB.ExecContext(ctx, `UPDATE products SET sold = MAX(sold - 1, 0) WHERE id = ?`, s.ProductID)
	if orderID > 0 {
		m.Refund(ctx, orderID, "بازگشت وجه: سرور خارج وصل نشد")
	}
	m.notify(ctx, s.UserID, "service.cancelled", "سفارش لغو شد",
		fmt.Sprintf("سرور خارج شما برای سرویس #%d در مهلت تعیین‌شده وصل نشد؛ سفارش لغو و مبلغ به کیف پول برگشت.", id))
}

// billPayg charges pay-as-you-go services hourly: elapsed days × daily rate +
// new GB × per-GB rate. A wallet that cannot pay stops the service.
func (m *Manager) billPayg(ctx context.Context) {
	now := m.now()
	gb := m.gbBase(ctx)
	for _, id := range m.ids(ctx, `SELECT id FROM services WHERE model = 'payg' AND status = 'active' AND payg_billed_at <= ?`, now-3600) {
		s, err := m.service(ctx, id)
		if err != nil {
			continue
		}
		used := s.UsedUp + s.UsedDown
		var cost float64
		if s.PaygDay > 0 {
			cost += float64(s.PaygDay) * float64(now-s.PaygAt) / 86400
		}
		billedGB := used - s.PaygBilled
		if s.PaygGB > 0 && billedGB > 0 {
			cost += float64(s.PaygGB) * float64(billedGB) / float64(gb)
		}
		amount := int64(cost)
		err = m.DB.Tx(ctx, func(tx *sql.Tx) error {
			if amount > 0 {
				if _, err := wallet.ApplyTx(ctx, tx, wallet.Entry{UserID: s.UserID, Amount: -amount, Type: "payg", RefType: "service",
					RefID: strconv.FormatInt(id, 10), Note: fmt.Sprintf("مصرف سرویس #%d", id),
					IdempotencyKey: fmt.Sprintf("payg:%d:%d", id, now/3600)}); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `UPDATE services SET payg_billed_bytes = ?, payg_billed_at = ? WHERE id = ?`, used, now, id)
			return err
		})
		if err == wallet.ErrInsufficient {
			m.SetStatus(ctx, id, StLimited, "موجودی کیف پول تمام شد")
		}
	}
}

// ResumePayg reactivates pay-as-you-go services once the wallet is positive again.
func (m *Manager) ResumePayg(ctx context.Context, userID int64) {
	bal, _ := m.Wallet.Balance(ctx, userID)
	if bal <= 0 {
		return
	}
	for _, id := range m.ids(ctx, `SELECT id FROM services WHERE user_id = ? AND model = 'payg' AND status = 'limited'`, userID) {
		m.DB.ExecContext(ctx, `UPDATE services SET payg_billed_at = ? WHERE id = ?`, m.now(), id)
		m.SetStatus(ctx, id, StActive, "")
	}
}

// Stats is the admin dashboard summary.
type Stats struct {
	Users          int64            `json:"users"`
	UsersToday     int64            `json:"users_today"`
	Services       map[string]int64 `json:"services"`
	SalesToday     int64            `json:"sales_today"`
	SalesMonth     int64            `json:"sales_month"`
	PendingReceipt int64            `json:"pending_receipts"`
	OpenTickets    int64            `json:"open_tickets"`
	NodesOnline    int64            `json:"nodes_online"`
	NodesTotal     int64            `json:"nodes_total"`
	TrafficToday   int64            `json:"traffic_today"`
	SalesByDay     []DayAmount      `json:"sales_by_day"`
}

type DayAmount struct {
	Day    int64 `json:"day"`
	Amount int64 `json:"amount"`
}

func (m *Manager) Stats(ctx context.Context) Stats {
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	var s Stats
	q := func(dst *int64, query string, args ...any) { m.DB.QueryRowContext(ctx, query, args...).Scan(dst) }
	q(&s.Users, `SELECT COUNT(1) FROM users`)
	q(&s.UsersToday, `SELECT COUNT(1) FROM users WHERE created_at >= ?`, dayStart)
	q(&s.SalesToday, `SELECT COALESCE(SUM(amount),0) FROM orders WHERE status = 'paid' AND created_at >= ?`, dayStart)
	q(&s.SalesMonth, `SELECT COALESCE(SUM(amount),0) FROM orders WHERE status = 'paid' AND created_at >= ?`, monthStart)
	q(&s.PendingReceipt, `SELECT COUNT(1) FROM payments WHERE status = 'submitted'`)
	q(&s.OpenTickets, `SELECT COUNT(1) FROM tickets WHERE status = 'open'`)
	q(&s.NodesOnline, `SELECT COUNT(1) FROM nodes WHERE status = 'online' AND enabled = 1`)
	q(&s.NodesTotal, `SELECT COUNT(1) FROM nodes WHERE enabled = 1`)
	q(&s.TrafficToday, `SELECT COALESCE(SUM(up+down),0) FROM usage_hourly WHERE hour >= ?`, dayStart)
	s.Services = map[string]int64{}
	rows, _ := m.DB.QueryContext(ctx, `SELECT status, COUNT(1) FROM services WHERE status != 'deleted' GROUP BY status`)
	if rows != nil {
		for rows.Next() {
			var st string
			var n int64
			rows.Scan(&st, &n)
			s.Services[st] = n
		}
		rows.Close()
	}
	rows, _ = m.DB.QueryContext(ctx, `SELECT (created_at - ?) / 86400, SUM(amount) FROM orders WHERE status = 'paid' AND created_at >= ? GROUP BY 1 ORDER BY 1`, dayStart-29*86400, dayStart-29*86400)
	if rows != nil {
		for rows.Next() {
			var d DayAmount
			rows.Scan(&d.Day, &d.Amount)
			d.Day = dayStart - 29*86400 + d.Day*86400
			s.SalesByDay = append(s.SalesByDay, d)
		}
		rows.Close()
	}
	return s
}

var _ = json.Marshal
