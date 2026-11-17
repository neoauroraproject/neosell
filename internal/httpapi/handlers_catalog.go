package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"neosell/internal/audit"
	"neosell/internal/catalog"
	"neosell/internal/payments"
	"neosell/internal/pricing"
	"neosell/internal/rbac"
	"neosell/internal/wallet"
)

// resource registers list/create/update/delete routes for one admin entity.
type resource[T any] struct {
	path   string // e.g. "/api/admin/groups"
	perm   rbac.Perm
	kind   string // audit target type
	list   func(context.Context) ([]T, error)
	save   func(context.Context, T) (int64, error)
	del    func(context.Context, int64) error
	withID func(*T, int64)
}

func register[T any](s *Server, mux *http.ServeMux, res resource[T]) {
	mux.Handle("GET "+res.path, s.admin(res.perm, func(w http.ResponseWriter, r *http.Request) {
		items, err := res.list(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	}))
	save := func(w http.ResponseWriter, r *http.Request) {
		var in T
		if err := readJSON(r, &in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		id := pathID(r)
		res.withID(&in, id) // create: 0, update: path id (body id is ignored)
		newID, err := res.save(r.Context(), in)
		if err != nil {
			code := 400
			if errors.Is(err, catalog.ErrNotFound) {
				code = 404
			}
			writeErr(w, code, err.Error())
			return
		}
		action := res.kind + ".update"
		if id == 0 {
			action = res.kind + ".create"
		}
		s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: action, TargetType: res.kind, TargetID: strconv.FormatInt(newID, 10), After: in, IP: clientIP(r)})
		writeJSON(w, 200, map[string]int64{"id": newID})
	}
	mux.Handle("POST "+res.path, s.admin(res.perm, save))
	mux.Handle("PUT "+res.path+"/{id}", s.admin(res.perm, save))
	mux.Handle("DELETE "+res.path+"/{id}", s.admin(res.perm, func(w http.ResponseWriter, r *http.Request) {
		if err := res.del(r.Context(), pathID(r)); err != nil {
			code := 400
			if errors.Is(err, catalog.ErrNotFound) {
				code = 404
			}
			writeErr(w, code, err.Error())
			return
		}
		s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: res.kind + ".delete", TargetType: res.kind, TargetID: r.PathValue("id"), IP: clientIP(r)})
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
}

func (s *Server) registerCatalog(mux *http.ServeMux) {
	c := s.Catalog
	register(s, mux, resource[catalog.Group]{path: "/api/admin/groups", perm: rbac.PricingManage, kind: "group",
		list: c.Groups, save: c.SaveGroup, del: c.DeleteGroup, withID: func(v *catalog.Group, id int64) { v.ID = id }})
	register(s, mux, resource[catalog.Location]{path: "/api/admin/locations", perm: rbac.ProductsManage, kind: "location",
		list: c.Locations, save: c.SaveLocation, del: c.DeleteLocation, withID: func(v *catalog.Location, id int64) { v.ID = id }})
	register(s, mux, resource[catalog.PortClass]{path: "/api/admin/port-classes", perm: rbac.PricingManage, kind: "port_class",
		list: c.PortClasses, save: c.SavePortClass, del: c.DeletePortClass, withID: func(v *catalog.PortClass, id int64) { v.ID = id }})
	register(s, mux, resource[catalog.Product]{path: "/api/admin/products", perm: rbac.ProductsManage, kind: "product",
		list: c.Products, save: c.SaveProduct, del: c.DeleteProduct, withID: func(v *catalog.Product, id int64) { v.ID = id }})
	register(s, mux, resource[pricing.Rule]{path: "/api/admin/price-rules", perm: rbac.PricingManage, kind: "price_rule",
		list: func(ctx context.Context) ([]pricing.Rule, error) { return c.Rules(ctx, catalog.RuleFilter{}) },
		save: c.SaveRule, del: c.DeleteRule, withID: func(v *pricing.Rule, id int64) { v.ID = id }})
	register(s, mux, resource[catalog.Discount]{path: "/api/admin/discounts", perm: rbac.PricingManage, kind: "discount",
		list: c.Discounts, save: c.SaveDiscount, del: c.DeleteDiscount, withID: func(v *catalog.Discount, id int64) { v.ID = id }})
	register(s, mux, resource[payments.Card]{path: "/api/admin/cards", perm: rbac.SettingsManage, kind: "bank_card",
		list: func(ctx context.Context) ([]payments.Card, error) { return s.Payments.Cards(ctx, false) },
		save: s.Payments.SaveCard, del: s.Payments.DeleteCard, withID: func(v *payments.Card, id int64) { v.ID = id }})

	mux.Handle("GET /api/admin/pricing/meta", s.admin(rbac.PricingManage, s.handlePricingMeta))
	mux.Handle("POST /api/admin/pricing/simulate", s.admin(rbac.PricingManage, s.handlePricingSimulate))

	mux.Handle("GET /api/admin/payments", s.admin(rbac.PaymentsApprove, s.handleAdminPayments))
	mux.Handle("GET /api/admin/payments/{id}/receipt", s.admin(rbac.PaymentsApprove, func(w http.ResponseWriter, r *http.Request) { s.serveReceipt(w, r, false) }))
	mux.Handle("POST /api/admin/payments/{id}/approve", s.admin(rbac.PaymentsApprove, s.handlePaymentApprove))
	mux.Handle("POST /api/admin/payments/{id}/reject", s.admin(rbac.PaymentsApprove, s.handlePaymentReject))

	mux.Handle("GET /api/admin/users", s.admin(rbac.UsersView, s.handleUsersList))
	mux.Handle("GET /api/admin/users/{id}", s.admin(rbac.UsersView, s.handleUserGet))
	mux.Handle("PUT /api/admin/users/{id}", s.admin(rbac.UsersManage, s.handleUserUpdate))
	mux.Handle("POST /api/admin/users/{id}/wallet", s.admin(rbac.UsersWallet, s.handleUserWallet))
}

// --- pricing ---

var componentLabels = map[string]string{
	pricing.CompPackage: "قیمت بسته (جایگزین قیمت فهرست)", pricing.CompBase: "هزینه‌ی پایه‌ی هر سفارش",
	pricing.CompPort: "قیمت هر پورت", pricing.CompGB: "قیمت هر گیگ (پلکانی)", pricing.CompLocation: "قیمت هر لوکیشن",
	pricing.CompRate: "قیمت هر Mbps سرعت", pricing.CompIP: "قیمت هر IP اضافه", pricing.CompDuration: "ضریب مدت",
	pricing.CompAddon: "قیمت افزودنی", pricing.CompRenew: "ضریب تمدید", pricing.CompSale: "فروش ویژه (درصد)",
	pricing.CompPaygGB: "پس‌پرداخت: هر گیگ مصرفی", pricing.CompPaygDay: "پس‌پرداخت: هر روز",
}

var modelLabels = map[pricing.Model]string{
	pricing.ModelPackage: "بسته‌ی ثابت", pricing.ModelBuilder: "انتخاب مقادیر توسط مشتری", pricing.ModelPerPort: "قیمت به‌ازای پورت",
	pricing.ModelPerGB: "فقط حجم", pricing.ModelUnlimited: "نامحدود با سقف سرعت", pricing.ModelPayg: "پرداخت به‌ازای مصرف",
}

func (s *Server) handlePricingMeta(w http.ResponseWriter, r *http.Request) {
	comps := []map[string]string{}
	for _, c := range pricing.AllComponents {
		comps = append(comps, map[string]string{"id": c, "label": componentLabels[c]})
	}
	models := []map[string]string{}
	for _, m := range pricing.AllModels {
		models = append(models, map[string]string{"id": string(m), "label": modelLabels[m]})
	}
	writeJSON(w, 200, map[string]any{
		"components": comps, "models": models,
		"scope_keys": []string{"class_id", "location_id", "package_id", "addon_kind", "core", "proto", "delivery", "node_id", "model"},
	})
}

// POST /api/admin/pricing/simulate {product_id, selection, user_id?, group_id?}
// Prices exactly as the shop would for that customer — including hidden products.
func (s *Server) handlePricingSimulate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		catalog.QuoteInput
		UserID  int64 `json:"user_id"`
		GroupID int64 `json:"group_id"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c := catalog.Customer{UserID: in.UserID, GroupID: in.GroupID}
	if in.UserID > 0 && in.GroupID == 0 {
		c.GroupID = s.userGroup(ctx, in.UserID)
	}
	_, q, err := s.quote(ctx, &in.QuoteInput, c)
	if err != nil {
		writePricingErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"quote": q, "selection": in.Selection})
}

// --- payments (admin) ---

func (s *Server) handleAdminPayments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	uid, _ := strconv.ParseInt(q.Get("user_id"), 10, 64)
	list, err := s.Payments.List(r.Context(), payments.ListFilter{Status: q.Get("status"), UserID: uid, BeforeID: before, Limit: 50})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handlePaymentApprove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note   string `json:"note"`
		Amount int64  `json:"amount"` // optional: credit a different amount than requested
	}
	readJSON(r, &in)
	p, err := s.Payments.Approve(r.Context(), pathID(r), currentUser(r).ID, in.Note, in.Amount)
	if err != nil {
		code := 400
		if errors.Is(err, payments.ErrNotFound) {
			code = 404
		}
		writeErr(w, code, err.Error())
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "payment.approve", TargetType: "payment", TargetID: r.PathValue("id"), After: in, IP: clientIP(r)})
	s.notifyAdmins(r.Context(), "payment.approved", p)
	writeJSON(w, 200, p)
}

func (s *Server) handlePaymentReject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	readJSON(r, &in)
	p, err := s.Payments.Reject(r.Context(), pathID(r), currentUser(r).ID, in.Note)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "payment.reject", TargetType: "payment", TargetID: r.PathValue("id"), After: in, IP: clientIP(r)})
	s.notifyAdmins(r.Context(), "payment.rejected", p)
	writeJSON(w, 200, p)
}

// --- users (admin) ---

type userRow struct {
	ID               int64  `json:"id"`
	Email            string `json:"email"`
	Name             string `json:"name"`
	Phone            string `json:"phone"`
	Status           string `json:"status"`
	IsAdmin          bool   `json:"is_admin"`
	GroupID          int64  `json:"group_id"`
	CreditLimit      int64  `json:"credit_limit"`
	Balance          int64  `json:"balance"`
	TelegramUsername string `json:"telegram_username"`
	CreatedAt        int64  `json:"created_at"`
}

const userRowCols = `u.id, COALESCE(u.email,''), u.name, COALESCE(u.phone,''), u.status, u.is_admin, COALESCE(u.group_id,0),
	u.credit_limit, COALESCE(w.balance,0), COALESCE(u.telegram_username,''), u.created_at`

func scanUserRow(row interface{ Scan(...any) error }) (userRow, error) {
	var u userRow
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Phone, &u.Status, &u.IsAdmin, &u.GroupID, &u.CreditLimit, &u.Balance, &u.TelegramUsername, &u.CreatedAt)
	return u, err
}

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sqlq := `SELECT ` + userRowCols + ` FROM users u LEFT JOIN wallets w ON w.user_id = u.id WHERE 1=1`
	args := []any{}
	if term := strings.TrimSpace(q.Get("q")); term != "" {
		like := "%" + strings.NewReplacer("%", `\%`, "_", `\_`).Replace(term) + "%"
		sqlq += ` AND (u.email LIKE ? ESCAPE '\' OR u.name LIKE ? ESCAPE '\' OR u.phone LIKE ? ESCAPE '\' OR u.telegram_username LIKE ? ESCAPE '\' OR CAST(u.id AS TEXT) = ?)`
		args = append(args, like, like, like, like, term)
	}
	if g, _ := strconv.ParseInt(q.Get("group_id"), 10, 64); g > 0 {
		sqlq += ` AND u.group_id = ?`
		args = append(args, g)
	}
	if b, _ := strconv.ParseInt(q.Get("before"), 10, 64); b > 0 {
		sqlq += ` AND u.id < ?`
		args = append(args, b)
	}
	sqlq += ` ORDER BY u.id DESC LIMIT 50`
	rows, err := s.DB.QueryContext(r.Context(), sqlq, args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []userRow{}
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, u)
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) getUserRow(ctx context.Context, id int64) (userRow, error) {
	return scanUserRow(s.DB.QueryRowContext(ctx, `SELECT `+userRowCols+` FROM users u LEFT JOIN wallets w ON w.user_id = u.id WHERE u.id = ?`, id))
}

func (s *Server) handleUserGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, err := s.getUserRow(ctx, pathID(r))
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "کاربر پیدا نشد")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	txs, _ := s.Wallet.History(ctx, u.ID, 0, 50)
	pays, _ := s.Payments.List(ctx, payments.ListFilter{UserID: u.ID, Limit: 20})
	writeJSON(w, 200, map[string]any{"user": u, "transactions": txs, "payments": pays})
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r)
	before, err := s.getUserRow(ctx, id)
	if err != nil {
		writeErr(w, 404, "کاربر پیدا نشد")
		return
	}
	var in struct {
		Name        *string `json:"name"`
		Phone       *string `json:"phone"`
		Status      *string `json:"status"`
		GroupID     *int64  `json:"group_id"`
		CreditLimit *int64  `json:"credit_limit"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if before.IsAdmin && !currentUser(r).IsOwner && in.Status != nil {
		writeErr(w, 403, "وضعیت ادمین‌ها را فقط مالک تغییر می‌دهد")
		return
	}
	sets, args := []string{}, []any{}
	if in.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, strings.TrimSpace(*in.Name))
	}
	if in.Phone != nil {
		sets, args = append(sets, "phone = ?"), append(args, strings.TrimSpace(*in.Phone))
	}
	if in.Status != nil {
		if *in.Status != "active" && *in.Status != "blocked" {
			writeErr(w, 400, "وضعیت نامعتبر است")
			return
		}
		sets, args = append(sets, "status = ?"), append(args, *in.Status)
	}
	if in.GroupID != nil {
		var g any
		if *in.GroupID > 0 {
			var n int
			s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM user_groups WHERE id = ?`, *in.GroupID).Scan(&n)
			if n == 0 {
				writeErr(w, 400, "گروه پیدا نشد")
				return
			}
			g = *in.GroupID
		}
		sets, args = append(sets, "group_id = ?"), append(args, g)
	}
	if in.CreditLimit != nil {
		if *in.CreditLimit < 0 {
			writeErr(w, 400, "سقف بدهی نمی‌تواند منفی باشد")
			return
		}
		sets, args = append(sets, "credit_limit = ?"), append(args, *in.CreditLimit)
	}
	if len(sets) == 0 {
		writeErr(w, 400, "تغییری ارسال نشد")
		return
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().Unix(), id)
	if _, err := s.DB.ExecContext(ctx, `UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if in.Status != nil && *in.Status == "blocked" {
		s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
	}
	after, _ := s.getUserRow(ctx, id)
	s.Audit.Write(ctx, audit.Entry{ActorID: currentUser(r).ID, Action: "user.update", TargetType: "user", TargetID: strconv.FormatInt(id, 10), Before: before, After: after, IP: clientIP(r)})
	writeJSON(w, 200, after)
}

// POST /api/admin/users/{id}/wallet {amount (+/-), note, allow_negative}
func (s *Server) handleUserWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r)
	if _, err := s.getUserRow(ctx, id); err != nil {
		writeErr(w, 404, "کاربر پیدا نشد")
		return
	}
	var in struct {
		Amount        int64  `json:"amount"`
		Note          string `json:"note"`
		AllowNegative bool   `json:"allow_negative"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Note) == "" {
		writeErr(w, 400, "دلیل تغییر موجودی را بنویسید")
		return
	}
	tx, err := s.Wallet.Apply(ctx, wallet.Entry{UserID: id, Amount: in.Amount, Type: "admin", Note: in.Note,
		CreatedBy: currentUser(r).ID, AllowNegative: in.AllowNegative})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Audit.Write(ctx, audit.Entry{ActorID: currentUser(r).ID, Action: "wallet.adjust", TargetType: "user", TargetID: strconv.FormatInt(id, 10), After: tx, IP: clientIP(r)})
	writeJSON(w, 200, tx)
}
