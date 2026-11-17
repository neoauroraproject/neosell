// Package catalog stores what the shop sells and how it is priced: user
// groups, locations, port classes, products, price rules and coupons. It also
// assembles the inputs the pricing engine needs for one customer.
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"neosell/internal/db"
	"neosell/internal/pricing"
)

var ErrNotFound = errors.New("پیدا نشد")

type Store struct{ db *db.DB }

func New(d *db.DB) *Store { return &Store{db: d} }

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func unjs[T any](s string, def T) T {
	var v T
	if s == "" || json.Unmarshal([]byte(s), &v) != nil {
		return def
	}
	return v
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- user groups ----------

type Group struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	DiscountPct float64 `json:"discount_pct"`
	Members     int     `json:"members"`
}

func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id, g.name, g.discount_pct, (SELECT COUNT(1) FROM users u WHERE u.group_id = g.id) FROM user_groups g ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.DiscountPct, &g.Members); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) SaveGroup(ctx context.Context, g Group) (int64, error) {
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" || g.DiscountPct < 0 || g.DiscountPct > 100 {
		return 0, errors.New("نام گروه و درصد تخفیف (۰ تا ۱۰۰) را درست وارد کنید")
	}
	if g.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO user_groups(name, discount_pct, created_at) VALUES (?, ?, ?)`, g.Name, g.DiscountPct, db.Now())
		if err != nil {
			return 0, errors.New("نام گروه تکراری است")
		}
		return res.LastInsertId()
	}
	return g.ID, affected(s.db.ExecContext(ctx, `UPDATE user_groups SET name = ?, discount_pct = ? WHERE id = ?`, g.Name, g.DiscountPct, g.ID))
}

func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET group_id = NULL WHERE group_id = ?`, id); err != nil {
			return err
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM user_groups WHERE id = ?`, id))
	})
}

// ---------- locations ----------

type Location struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Flag string `json:"flag"`
	Sort int    `json:"sort"`
}

func (s *Store) Locations(ctx context.Context) ([]Location, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, flag, sort FROM locations ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.ID, &l.Name, &l.Flag, &l.Sort); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) SaveLocation(ctx context.Context, l Location) (int64, error) {
	if strings.TrimSpace(l.Name) == "" {
		return 0, errors.New("نام لوکیشن را وارد کنید")
	}
	if l.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO locations(name, flag, sort) VALUES (?, ?, ?)`, l.Name, l.Flag, l.Sort)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	return l.ID, affected(s.db.ExecContext(ctx, `UPDATE locations SET name = ?, flag = ?, sort = ? WHERE id = ?`, l.Name, l.Flag, l.Sort, l.ID))
}

func (s *Store) DeleteLocation(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM locations WHERE id = ?`, id))
}

// ---------- port classes ----------

type PortClass struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // specific | range | random
	Expr string `json:"expr"`
	Sort int    `json:"sort"`
}

func (s *Store) PortClasses(ctx context.Context) ([]PortClass, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, expr, sort FROM port_classes ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PortClass{}
	for rows.Next() {
		var c PortClass
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Expr, &c.Sort); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SavePortClass(ctx context.Context, c PortClass) (int64, error) {
	c.Name, c.Expr = strings.TrimSpace(c.Name), strings.ReplaceAll(c.Expr, " ", "")
	if c.Name == "" {
		return 0, errors.New("نام کلاس پورت را وارد کنید")
	}
	switch c.Kind {
	case "specific":
		if _, err := ParsePortList(c.Expr); err != nil {
			return 0, err
		}
	case "range":
		if _, _, err := ParseRange(c.Expr); err != nil {
			return 0, err
		}
	case "random":
		c.Expr = ""
	default:
		return 0, errors.New("نوع کلاس پورت نامعتبر است")
	}
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO port_classes(name, kind, expr, sort) VALUES (?, ?, ?, ?)`, c.Name, c.Kind, c.Expr, c.Sort)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	return c.ID, affected(s.db.ExecContext(ctx, `UPDATE port_classes SET name=?, kind=?, expr=?, sort=? WHERE id=?`, c.Name, c.Kind, c.Expr, c.Sort, c.ID))
}

func (s *Store) DeletePortClass(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM port_classes WHERE id = ?`, id))
}

func parsePort(p string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(p))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("پورت نامعتبر: %s", p)
	}
	return n, nil
}

func ParsePortList(expr string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(expr, ",") {
		if p == "" {
			continue
		}
		n, err := parsePort(p)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, errors.New("حداقل یک پورت وارد کنید")
	}
	return out, nil
}

func ParseRange(expr string) (int, int, error) {
	a, b, ok := strings.Cut(expr, "-")
	if !ok {
		return 0, 0, errors.New("بازه باید به شکل 2000-2999 باشد")
	}
	lo, err := parsePort(a)
	if err != nil {
		return 0, 0, err
	}
	hi, err := parsePort(b)
	if err != nil {
		return 0, 0, err
	}
	if lo > hi {
		return 0, 0, errors.New("ابتدای بازه بزرگ‌تر از انتهای آن است")
	}
	return lo, hi, nil
}

// ExpandPorts turns "443", "8080,8443" or "2053-2056" into single ports
// (at most max; max ≤ 0 means 1000).
func ExpandPorts(expr string, max int) ([]int, error) {
	if max <= 0 {
		max = 1000
	}
	var out []int
	for _, part := range strings.Split(strings.ReplaceAll(expr, " ", ""), ",") {
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			lo, hi, err := ParseRange(part)
			if err != nil {
				return nil, err
			}
			for p := lo; p <= hi; p++ {
				out = append(out, p)
				if len(out) > max {
					return nil, fmt.Errorf("حداکثر %d پورت", max)
				}
			}
			continue
		}
		n, err := parsePort(part)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	seen := map[int]bool{}
	for _, p := range out {
		if seen[p] {
			return nil, fmt.Errorf("پورت %d تکراری است", p)
		}
		seen[p] = true
	}
	if len(out) > max {
		return nil, fmt.Errorf("حداکثر %d پورت", max)
	}
	return out, nil
}

// Classify finds the class of a requested port: a specific list wins over a
// range; an empty request means "random". It fails when no class covers it,
// which is how the admin restricts which ports are sellable.
func Classify(classes []PortClass, port string) (int64, error) {
	port = strings.TrimSpace(port)
	if port == "" {
		for _, c := range classes {
			if c.Kind == "random" {
				return c.ID, nil
			}
		}
		return 0, errors.New("پورت تصادفی فروخته نمی‌شود؛ پورت را مشخص کنید")
	}
	n, err := parsePort(port)
	if err != nil {
		return 0, err
	}
	for _, c := range classes {
		if c.Kind == "specific" {
			if ps, err := ParsePortList(c.Expr); err == nil && slices.Contains(ps, n) {
				return c.ID, nil
			}
		}
	}
	for _, c := range classes {
		if c.Kind == "range" {
			if lo, hi, err := ParseRange(c.Expr); err == nil && n >= lo && n <= hi {
				return c.ID, nil
			}
		}
	}
	return 0, fmt.Errorf("پورت %d قابل فروش نیست", n)
}

// ---------- products ----------

type Product struct {
	ID            int64           `json:"id"`
	Type          string          `json:"type"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	Enabled       bool            `json:"enabled"`
	VisibleGroups []int64         `json:"visible_groups"`
	Stock         int64           `json:"stock"`
	Sold          int64           `json:"sold"`
	Sort          int             `json:"sort"`
	Options       pricing.Options `json:"options"`
	CreatedAt     int64           `json:"created_at"`
}

func (p *Product) Pricing() pricing.Product {
	return pricing.Product{ID: p.ID, Type: p.Type, Title: p.Title, Options: p.Options}
}

// VisibleTo reports whether a customer in group gid may see and buy it.
func (p *Product) VisibleTo(gid int64) bool {
	return p.Enabled && (len(p.VisibleGroups) == 0 || slices.Contains(p.VisibleGroups, gid)) &&
		(p.Stock < 0 || p.Sold < p.Stock)
}

const productCols = `id, type, title, description, enabled, visible_groups, stock, sold, sort, options, created_at`

func scanProduct(row interface{ Scan(...any) error }) (*Product, error) {
	p := &Product{}
	var vg, opts string
	if err := row.Scan(&p.ID, &p.Type, &p.Title, &p.Description, &p.Enabled, &vg, &p.Stock, &p.Sold, &p.Sort, &opts, &p.CreatedAt); err != nil {
		return nil, err
	}
	p.VisibleGroups = unjs(vg, []int64{})
	p.Options = unjs(opts, pricing.Options{})
	if p.Options.Models == nil {
		p.Options.Models = []pricing.Model{}
	}
	return p, nil
}

func (s *Store) Products(ctx context.Context) ([]Product, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+productCols+` FROM products ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) Product(ctx context.Context, id int64) (*Product, error) {
	p, err := scanProduct(s.db.QueryRowContext(ctx, `SELECT `+productCols+` FROM products WHERE id = ?`, id))
	return p, notFound(err)
}

func validateProduct(p *Product) error {
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" {
		return errors.New("عنوان محصول را وارد کنید")
	}
	if p.Type != "port" && p.Type != "outbound" {
		return errors.New("نوع محصول باید port یا outbound باشد")
	}
	o := &p.Options
	if len(o.Models) == 0 {
		return errors.New("حداقل یک روش فروش انتخاب کنید")
	}
	for _, m := range o.Models {
		if !slices.Contains(pricing.AllModels, m) {
			return fmt.Errorf("روش فروش ناشناخته: %s", m)
		}
	}
	if slices.Contains(o.Models, pricing.ModelPackage) && len(o.Packages) == 0 {
		return errors.New("برای فروش بسته‌ای حداقل یک بسته تعریف کنید")
	}
	ids := map[string]bool{}
	for i := range o.Packages {
		pk := &o.Packages[i]
		pk.ID = strings.TrimSpace(pk.ID)
		if pk.ID == "" || ids[pk.ID] {
			return errors.New("هر بسته شناسه‌ی یکتا لازم دارد")
		}
		ids[pk.ID] = true
		if pk.Days <= 0 || pk.Price < 0 || pk.GB < 0 {
			return fmt.Errorf("بسته‌ی %s: مدت و قیمت را درست وارد کنید", pk.Name)
		}
		if p.Type == "port" && pk.Ports <= 0 {
			return fmt.Errorf("بسته‌ی %s: تعداد پورت را وارد کنید", pk.Name)
		}
		if p.Type == "outbound" && len(pk.Locations) == 0 {
			return fmt.Errorf("بسته‌ی %s: حداقل یک لوکیشن لازم است", pk.Name)
		}
	}
	for _, d := range o.Durations {
		if d <= 0 {
			return errors.New("مدت‌ها باید مثبت باشند")
		}
	}
	if o.MaxPorts > 0 && o.MinPorts > o.MaxPorts || o.MaxGB > 0 && o.MinGB > o.MaxGB || o.MaxDays > 0 && o.MinDays > o.MaxDays {
		return errors.New("حداقل‌ها نباید از حداکثرها بیشتر باشند")
	}
	if p.VisibleGroups == nil {
		p.VisibleGroups = []int64{}
	}
	return nil
}

func (s *Store) SaveProduct(ctx context.Context, p Product) (int64, error) {
	if err := validateProduct(&p); err != nil {
		return 0, err
	}
	now := db.Now()
	if p.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO products(type, title, description, enabled, visible_groups, stock, sort, options, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.Type, p.Title, p.Description, p.Enabled, js(p.VisibleGroups), p.Stock, p.Sort, js(p.Options), now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	return p.ID, affected(s.db.ExecContext(ctx, `UPDATE products SET type=?, title=?, description=?, enabled=?, visible_groups=?, stock=?, sort=?, options=?, updated_at=? WHERE id=?`,
		p.Type, p.Title, p.Description, p.Enabled, js(p.VisibleGroups), p.Stock, p.Sort, js(p.Options), now, p.ID))
}

// DeleteProduct disables instead of deleting when it has history (later: orders).
func (s *Store) DeleteProduct(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM products WHERE id = ?`, id))
}

// ---------- price rules ----------

const ruleCols = `id, COALESCE(product_id,0), component, scope, value, tiers, COALESCE(group_id,0), COALESCE(user_id,0), priority, valid_from, valid_to, enabled, note`

func scanRule(row interface{ Scan(...any) error }) (*pricing.Rule, error) {
	r := &pricing.Rule{}
	var scope, tiers string
	if err := row.Scan(&r.ID, &r.ProductID, &r.Component, &scope, &r.Value, &tiers, &r.GroupID, &r.UserID, &r.Priority, &r.ValidFrom, &r.ValidTo, &r.Enabled, &r.Note); err != nil {
		return nil, err
	}
	r.Scope = unjs(scope, map[string]string{})
	r.Tiers = unjs(tiers, []pricing.Tier{})
	return r, nil
}

type RuleFilter struct {
	ProductID int64 // >0: rules of this product AND global ones
	Component string
}

func (s *Store) Rules(ctx context.Context, f RuleFilter) ([]pricing.Rule, error) {
	q := `SELECT ` + ruleCols + ` FROM price_rules WHERE 1=1`
	args := []any{}
	if f.ProductID > 0 {
		q += ` AND (product_id IS NULL OR product_id = ?)`
		args = append(args, f.ProductID)
	}
	if f.Component != "" {
		q += ` AND component = ?`
		args = append(args, f.Component)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY component, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pricing.Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func (s *Store) SaveRule(ctx context.Context, r pricing.Rule) (int64, error) {
	if err := pricing.ValidateRule(r); err != nil {
		return 0, err
	}
	if r.Scope == nil {
		r.Scope = map[string]string{}
	}
	if r.Tiers == nil {
		r.Tiers = []pricing.Tier{}
	}
	now := db.Now()
	if r.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO price_rules(product_id, component, scope, value, tiers, group_id, user_id, priority, valid_from, valid_to, enabled, note, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, nullID(r.ProductID), r.Component, js(r.Scope), r.Value, js(r.Tiers),
			nullID(r.GroupID), nullID(r.UserID), r.Priority, r.ValidFrom, r.ValidTo, r.Enabled, r.Note, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	return r.ID, affected(s.db.ExecContext(ctx, `UPDATE price_rules SET product_id=?, component=?, scope=?, value=?, tiers=?, group_id=?, user_id=?, priority=?, valid_from=?, valid_to=?, enabled=?, note=?, updated_at=? WHERE id=?`,
		nullID(r.ProductID), r.Component, js(r.Scope), r.Value, js(r.Tiers), nullID(r.GroupID), nullID(r.UserID), r.Priority, r.ValidFrom, r.ValidTo, r.Enabled, r.Note, now, r.ID))
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM price_rules WHERE id = ?`, id))
}

// ---------- discounts ----------

type Discount struct {
	ID             int64   `json:"id"`
	Code           string  `json:"code"`
	Kind           string  `json:"kind"`
	Value          float64 `json:"value"`
	MaxUses        int64   `json:"max_uses"`
	Used           int64   `json:"used"`
	PerUser        int64   `json:"per_user"`
	MinAmount      int64   `json:"min_amount"`
	ProductIDs     []int64 `json:"product_ids"`
	FirstOrderOnly bool    `json:"first_order_only"`
	ValidFrom      int64   `json:"valid_from"`
	ValidTo        int64   `json:"valid_to"`
	Enabled        bool    `json:"enabled"`
}

const discountCols = `id, code, kind, value, max_uses, used, per_user, min_amount, product_ids, first_order_only, valid_from, valid_to, enabled`

func scanDiscount(row interface{ Scan(...any) error }) (*Discount, error) {
	d := &Discount{}
	var pids string
	if err := row.Scan(&d.ID, &d.Code, &d.Kind, &d.Value, &d.MaxUses, &d.Used, &d.PerUser, &d.MinAmount, &pids, &d.FirstOrderOnly, &d.ValidFrom, &d.ValidTo, &d.Enabled); err != nil {
		return nil, err
	}
	d.ProductIDs = unjs(pids, []int64{})
	return d, nil
}

func (s *Store) Discounts(ctx context.Context) ([]Discount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+discountCols+` FROM discounts ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Discount{}
	for rows.Next() {
		d, err := scanDiscount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) SaveDiscount(ctx context.Context, d Discount) (int64, error) {
	d.Code = strings.ToUpper(strings.TrimSpace(d.Code))
	if d.Code == "" || len(d.Code) > 40 {
		return 0, errors.New("کد تخفیف را وارد کنید")
	}
	if d.Kind != "percent" && d.Kind != "amount" || d.Value <= 0 || d.Kind == "percent" && d.Value > 100 {
		return 0, errors.New("نوع و مقدار تخفیف نامعتبر است")
	}
	if d.ProductIDs == nil {
		d.ProductIDs = []int64{}
	}
	if d.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO discounts(code, kind, value, max_uses, per_user, min_amount, product_ids, first_order_only, valid_from, valid_to, enabled, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d.Code, d.Kind, d.Value, d.MaxUses, d.PerUser, d.MinAmount, js(d.ProductIDs), d.FirstOrderOnly, d.ValidFrom, d.ValidTo, d.Enabled, db.Now())
		if err != nil {
			return 0, errors.New("این کد قبلاً ثبت شده است")
		}
		return res.LastInsertId()
	}
	return d.ID, affected(s.db.ExecContext(ctx, `UPDATE discounts SET code=?, kind=?, value=?, max_uses=?, per_user=?, min_amount=?, product_ids=?, first_order_only=?, valid_from=?, valid_to=?, enabled=? WHERE id=?`,
		d.Code, d.Kind, d.Value, d.MaxUses, d.PerUser, d.MinAmount, js(d.ProductIDs), d.FirstOrderOnly, d.ValidFrom, d.ValidTo, d.Enabled, d.ID))
}

func (s *Store) DeleteDiscount(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM discounts WHERE id = ?`, id))
}

// CouponUsage tells ResolveCoupon how often this user used the code and
// whether they have ordered before. Orders arrive in the next phase; until
// then the shop passes zeros.
type CouponUsage struct {
	ByUser       int64
	HasOrderedOK bool
}

// ResolveCoupon validates a code for a product and user at time now.
func (s *Store) ResolveCoupon(ctx context.Context, code string, productID int64, now int64, use CouponUsage) (*pricing.Coupon, *Discount, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, nil, nil
	}
	d, err := scanDiscount(s.db.QueryRowContext(ctx, `SELECT `+discountCols+` FROM discounts WHERE code = ?`, code))
	if err != nil {
		return nil, nil, errors.New("کد تخفیف معتبر نیست")
	}
	switch {
	case !d.Enabled:
		return nil, nil, errors.New("کد تخفیف غیرفعال است")
	case d.ValidFrom > 0 && now < d.ValidFrom, d.ValidTo > 0 && now >= d.ValidTo:
		return nil, nil, errors.New("زمان استفاده از این کد نیست")
	case d.MaxUses > 0 && d.Used >= d.MaxUses:
		return nil, nil, errors.New("ظرفیت این کد تخفیف تمام شده است")
	case d.PerUser > 0 && use.ByUser >= d.PerUser:
		return nil, nil, errors.New("شما قبلاً از این کد استفاده کرده‌اید")
	case len(d.ProductIDs) > 0 && !slices.Contains(d.ProductIDs, productID):
		return nil, nil, errors.New("این کد برای این محصول نیست")
	case d.FirstOrderOnly && use.HasOrderedOK:
		return nil, nil, errors.New("این کد فقط برای اولین خرید است")
	}
	return &pricing.Coupon{Code: d.Code, Kind: d.Kind, Value: d.Value, MinAmount: d.MinAmount}, d, nil
}

// ---------- quoting ----------

// QuoteInput is what a customer (or the admin simulator) asks to price.
type QuoteInput struct {
	ProductID int64             `json:"product_id"`
	Selection pricing.Selection `json:"selection"`
}

type Customer struct {
	UserID  int64
	GroupID int64
}

// Prepare classifies requested ports and loads rules, group discount and
// coupon. It returns everything pricing.Price needs.
func (s *Store) Prepare(ctx context.Context, in *QuoteInput, c Customer, now, roundTo int64, use CouponUsage) (*Product, []pricing.Rule, pricing.Context, error) {
	pctx := pricing.Context{UserID: c.UserID, GroupID: c.GroupID, Now: now, RoundTo: roundTo}
	p, err := s.Product(ctx, in.ProductID)
	if err != nil {
		return nil, nil, pctx, pricing.Error{Msg: "محصول پیدا نشد"}
	}
	classes, err := s.PortClasses(ctx)
	if err != nil {
		return nil, nil, pctx, err
	}
	pctx.ClassNames = map[int64]string{}
	for _, cl := range classes {
		pctx.ClassNames[cl.ID] = cl.Name
	}
	// The server decides the class of each port; the client's class_id is ignored.
	seen := map[string]bool{}
	for i := range in.Selection.Ports {
		ps := &in.Selection.Ports[i]
		ps.Port = strings.TrimSpace(ps.Port)
		if ps.Port != "" {
			if seen[ps.Port] {
				return nil, nil, pctx, pricing.Error{Msg: "پورت " + ps.Port + " تکراری است"}
			}
			seen[ps.Port] = true
		}
		id, err := Classify(classes, ps.Port)
		if err != nil {
			return nil, nil, pctx, pricing.Error{Msg: err.Error()}
		}
		ps.ClassID = id
	}
	locs, err := s.Locations(ctx)
	if err != nil {
		return nil, nil, pctx, err
	}
	pctx.LocationNames = map[int64]string{}
	for _, l := range locs {
		pctx.LocationNames[l.ID] = strings.TrimSpace(l.Flag + " " + l.Name)
	}
	if c.GroupID > 0 {
		s.db.QueryRowContext(ctx, `SELECT discount_pct FROM user_groups WHERE id = ?`, c.GroupID).Scan(&pctx.GroupDiscountPct)
	}
	if in.Selection.Coupon != "" {
		cp, _, err := s.ResolveCoupon(ctx, in.Selection.Coupon, p.ID, now, use)
		if err != nil {
			return nil, nil, pctx, pricing.Error{Msg: err.Error()}
		}
		pctx.Coupon = cp
	}
	rules, err := s.Rules(ctx, RuleFilter{ProductID: p.ID})
	if err != nil {
		return nil, nil, pctx, err
	}
	return p, rules, pctx, nil
}
