// Package rbac: fine-grained admin permissions. The owner has every permission;
// other admins get the union of their roles' permissions.
package rbac

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"neosell/internal/auth"
	"neosell/internal/db"
)

type Perm string

const (
	SettingsManage  Perm = "settings.manage"
	PricingManage   Perm = "pricing.manage"
	ProductsManage  Perm = "products.manage"
	NodesManage     Perm = "nodes.manage"
	UsersView       Perm = "users.view"
	UsersManage     Perm = "users.manage"
	UsersWallet     Perm = "users.wallet"
	ServicesManage  Perm = "services.manage"
	OrdersView      Perm = "orders.view"
	PaymentsApprove Perm = "payments.approve"
	ReportsView     Perm = "reports.view"
	TicketsManage   Perm = "tickets.manage"
	MessagesManage  Perm = "messages.manage"
	BroadcastSend   Perm = "broadcast.send"
	AuditView       Perm = "audit.view"
	AdminsManage    Perm = "admins.manage" // owner only in practice (see Can)
)

// All lists every permission with a Persian label for the role editor.
var All = []struct {
	ID    Perm   `json:"id"`
	Label string `json:"label"`
}{
	{SettingsManage, "تنظیمات سراسری"},
	{PricingManage, "قیمت‌گذاری و مدل فروش"},
	{ProductsManage, "محصولات و افزودنی‌ها"},
	{NodesManage, "سرورها، Agentها و پنل‌های ثنایی"},
	{UsersView, "دیدن کاربران"},
	{UsersManage, "مدیریت کاربران"},
	{UsersWallet, "شارژ و کسر کیف پول"},
	{ServicesManage, "مدیریت سرویس‌ها"},
	{OrdersView, "دیدن سفارش‌ها"},
	{PaymentsApprove, "تأیید رسید و پرداخت"},
	{ReportsView, "گزارش‌های مالی"},
	{TicketsManage, "پشتیبانی و تیکت"},
	{MessagesManage, "متن پیام‌ها و ربات"},
	{BroadcastSend, "پیام همگانی"},
	{AuditView, "لاگ ممیزی"},
	{AdminsManage, "ادمین‌ها و نقش‌ها"},
}

func Valid(p Perm) bool {
	for _, x := range All {
		if x.ID == p {
			return true
		}
	}
	return false
}

type Store struct{ db *db.DB }

func NewStore(d *db.DB) *Store { return &Store{db: d} }

// Perms returns the effective permission set of a user.
func (s *Store) Perms(ctx context.Context, u *auth.User) ([]Perm, error) {
	if u == nil || !u.IsAdmin {
		return nil, nil
	}
	if u.IsOwner {
		out := make([]Perm, len(All))
		for i, x := range All {
			out[i] = x.ID
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.permissions FROM roles r JOIN user_roles ur ON ur.role_id = r.id WHERE ur.user_id = ?`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Perm
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var ps []Perm
		_ = json.Unmarshal([]byte(raw), &ps)
		for _, p := range ps {
			if Valid(p) && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out, rows.Err()
}

func (s *Store) Can(ctx context.Context, u *auth.User, p Perm) bool {
	if u == nil || !u.IsAdmin {
		return false
	}
	if u.IsOwner {
		return true
	}
	if p == AdminsManage {
		return false
	}
	ps, err := s.Perms(ctx, u)
	return err == nil && slices.Contains(ps, p)
}

type Role struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Permissions []Perm `json:"permissions"`
	Members     int    `json:"members"`
}

func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.name, r.permissions,
		(SELECT COUNT(1) FROM user_roles ur WHERE ur.role_id = r.id) FROM roles r ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		var r Role
		var raw string
		if err := rows.Scan(&r.ID, &r.Name, &raw, &r.Members); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &r.Permissions)
		out = append(out, r)
	}
	return out, rows.Err()
}

func clean(perms []Perm) []Perm {
	out := []Perm{}
	for _, p := range perms {
		if Valid(p) && p != AdminsManage && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) SaveRole(ctx context.Context, r Role) (int64, error) {
	r.Name = strings.TrimSpace(r.Name)
	raw, _ := json.Marshal(clean(r.Permissions))
	if r.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO roles(name, permissions, created_at) VALUES (?, ?, ?)`, r.Name, string(raw), db.Now())
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE roles SET name = ?, permissions = ? WHERE id = ?`, r.Name, string(raw), r.ID)
	return r.ID, err
}

func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id)
	return err
}

func (s *Store) SetUserRoles(ctx context.Context, userID int64, roleIDs []int64) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
			return err
		}
		for _, id := range roleIDs {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_roles(user_id, role_id) VALUES (?, ?)`, userID, id); err != nil {
				return err
			}
		}
		return nil
	})
}
