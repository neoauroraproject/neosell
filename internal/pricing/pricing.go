// Package pricing computes the price of an order from the admin's rules.
//
// It is pure (no I/O): the caller loads the product, its rules, the user's
// group and an optional coupon, and gets back an itemised quote. The same
// engine prices new orders, renewals and add-ons, and the admin "simulate"
// screen, so what the admin previews is exactly what the customer pays.
//
// Order of application:
//
//	component lines (package | ports, GB, locations, speed, IPs, base)
//	→ duration factor on time-based lines
//	→ renewal multiplier
//	→ user-group discount → time-limited sale → coupon → rounding
package pricing

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
)

// Model is a way of selling a product. A product enables one or more.
type Model string

const (
	ModelPackage   Model = "package"   // fixed bundles defined by the admin
	ModelBuilder   Model = "builder"   // customer picks ports, GB, days, speed …
	ModelPerPort   Model = "per_port"  // priced by port (class); traffic not charged
	ModelPerGB     Model = "per_gb"    // priced by GB only
	ModelUnlimited Model = "unlimited" // unlimited traffic with a speed cap
	ModelPayg      Model = "payg"      // nothing upfront; billed from the wallet by usage
)

var AllModels = []Model{ModelPackage, ModelBuilder, ModelPerPort, ModelPerGB, ModelUnlimited, ModelPayg}

// Components a rule can price.
const (
	CompPackage  = "package"  // scope package_id: overrides a package's list price
	CompBase     = "base"     // fixed amount per order (time-based)
	CompPort     = "port"     // per port; scope class_id (time-based)
	CompGB       = "gb"       // per GB, graduated tiers; scope location_id for outbound
	CompLocation = "location" // per outbound location; scope location_id (time-based)
	CompRate     = "rate"     // per Mbps of speed cap, graduated (time-based)
	CompIP       = "ip"       // per concurrent IP above the included ones (time-based)
	CompDuration = "duration" // tiers {days, mult}; default factor = days/30
	CompAddon    = "addon"    // scope addon_kind: price per unit of an add-on
	CompRenew    = "renew"    // value = multiplier for renewals (e.g. 0.9)
	CompSale     = "sale"     // value = percent off (use valid_from/to)
	CompPaygGB   = "payg_gb"  // pay-as-you-go: price per GB used
	CompPaygDay  = "payg_day" // pay-as-you-go: price per day per service
)

var AllComponents = []string{CompPackage, CompBase, CompPort, CompGB, CompLocation, CompRate, CompIP,
	CompDuration, CompAddon, CompRenew, CompSale, CompPaygGB, CompPaygDay}

type Tier struct {
	Upto  int     `json:"upto,omitempty"`  // graduated: units up to this (0 = no limit)
	Price float64 `json:"price,omitempty"` // graduated: price per unit in this band
	Days  int     `json:"days,omitempty"`  // duration: exact number of days
	Mult  float64 `json:"mult,omitempty"`  // duration: factor for those days
}

type Rule struct {
	ID        int64             `json:"id"`
	ProductID int64             `json:"product_id"` // 0 = every product
	Component string            `json:"component"`
	Scope     map[string]string `json:"scope"`
	Value     float64           `json:"value"`
	Tiers     []Tier            `json:"tiers"`
	GroupID   int64             `json:"group_id"`
	UserID    int64             `json:"user_id"`
	Priority  int               `json:"priority"`
	ValidFrom int64             `json:"valid_from"`
	ValidTo   int64             `json:"valid_to"`
	Enabled   bool              `json:"enabled"`
	Note      string            `json:"note"`
}

// PackageLoc is a location included in an outbound package.
type PackageLoc = LocSel

type Package struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Ports     int          `json:"ports"`
	GB        int          `json:"gb"` // 0 = unlimited
	Days      int          `json:"days"`
	RateMbit  int          `json:"rate_mbit"`
	MaxIPs    int          `json:"max_ips"`
	Price     int64        `json:"price"`
	Locations []PackageLoc `json:"locations,omitempty"`
}

// Options is a product's sale configuration (products.options JSON).
type Options struct {
	Models       []Model   `json:"models"`
	Packages     []Package `json:"packages,omitempty"`
	Durations    []int     `json:"durations,omitempty"` // allowed days; empty = MinDays..MaxDays
	MinDays      int       `json:"min_days,omitempty"`
	MaxDays      int       `json:"max_days,omitempty"`
	MinPorts     int       `json:"min_ports,omitempty"`
	MaxPorts     int       `json:"max_ports,omitempty"`
	MinGB        int       `json:"min_gb,omitempty"`
	MaxGB        int       `json:"max_gb,omitempty"`
	GBStep       int       `json:"gb_step,omitempty"`
	PerPortGB    int       `json:"per_port_gb,omitempty"` // per_port model: quota per port (0 = unlimited)
	PerGBDays    int       `json:"per_gb_days,omitempty"` // per_gb model: validity (default 365)
	MinRate      int       `json:"min_rate,omitempty"`
	MaxRate      int       `json:"max_rate,omitempty"`
	IncludedIPs  int       `json:"included_ips,omitempty"`
	MaxIPs       int       `json:"max_ips,omitempty"`
	Locations    []int64   `json:"locations,omitempty"` // outbound: allowed locations
	MinLocations int       `json:"min_locations,omitempty"`
	Cores        []string  `json:"cores,omitempty"`
	Protos       []string  `json:"protos,omitempty"`
	Deliveries   []string  `json:"deliveries,omitempty"`
	PaygMinTopup int64     `json:"payg_min_balance,omitempty"`
	// Nodes are the tunnel servers (port products) this product sells on;
	// empty = every tunnel server with sales enabled.
	Nodes []int64 `json:"nodes,omitempty"`
}

type Product struct {
	ID      int64   `json:"id"`
	Type    string  `json:"type"` // port | outbound
	Title   string  `json:"title"`
	Options Options `json:"options"`
}

type PortSel struct {
	ClassID int64  `json:"class_id"`
	Port    string `json:"port,omitempty"`    // requested port (empty = random)
	ToPort  int    `json:"to_port,omitempty"` // port on the destination server (0 = same)
}

type LocSel struct {
	LocationID int64 `json:"location_id"`
	GB         int   `json:"gb"`
}

type Selection struct {
	Kind      string    `json:"kind"` // new (default) | renew | addon
	Model     Model     `json:"model"`
	PackageID string    `json:"package_id,omitempty"`
	Ports     []PortSel `json:"ports,omitempty"`
	GB        int       `json:"gb,omitempty"`
	Days      int       `json:"days,omitempty"`
	RateMbit  int       `json:"rate_mbit,omitempty"`
	MaxIPs    int       `json:"max_ips,omitempty"`
	Locations []LocSel  `json:"locations,omitempty"`
	Core      string    `json:"core,omitempty"`
	Proto     string    `json:"proto,omitempty"`
	Delivery  string    `json:"delivery,omitempty"`
	NodeID    int64     `json:"node_id,omitempty"`
	// PeerAddress is the customer's foreign server IP (dedicated tunnels).
	PeerAddress string `json:"peer_address,omitempty"`
	AddonKind   string `json:"addon_kind,omitempty"` // gb | days | port | location | rate | ip
	AddonQty    int    `json:"addon_qty,omitempty"`
	Coupon      string `json:"coupon,omitempty"`
}

type Coupon struct {
	Code      string  `json:"code"`
	Kind      string  `json:"kind"` // percent | amount
	Value     float64 `json:"value"`
	MinAmount int64   `json:"min_amount"`
}

type Context struct {
	UserID           int64
	GroupID          int64
	GroupDiscountPct float64
	Now              int64
	RoundTo          int64 // e.g. 1000: round the total to the nearest 1000
	Coupon           *Coupon
	// Labels resolve ids to names for the itemised lines (optional).
	ClassNames    map[int64]string
	LocationNames map[int64]string
}

type Line struct {
	Component string  `json:"component"`
	Label     string  `json:"label"`
	Qty       float64 `json:"qty"`
	Unit      int64   `json:"unit"`
	Amount    int64   `json:"amount"`
	RuleID    int64   `json:"rule_id,omitempty"`
	timed     bool
}

// Spec is what the customer is buying, resolved from the selection or package.
type Spec struct {
	Model     Model    `json:"model"`
	Ports     int      `json:"ports"`
	GB        int      `json:"gb"` // 0 = unlimited
	Days      int      `json:"days"`
	RateMbit  int      `json:"rate_mbit"`
	MaxIPs    int      `json:"max_ips"`
	Locations []LocSel `json:"locations,omitempty"`
}

type Quote struct {
	Lines       []Line `json:"lines"`
	Subtotal    int64  `json:"subtotal"`
	Adjustments []Line `json:"adjustments"`
	Total       int64  `json:"total"`
	Spec        Spec   `json:"spec"`
	PaygPerGB   int64  `json:"payg_per_gb,omitempty"`
	PaygPerDay  int64  `json:"payg_per_day,omitempty"`
	PaygMinimum int64  `json:"payg_min_balance,omitempty"`
}

// Error is a customer-facing pricing/validation error.
type Error struct{ Msg string }

func (e Error) Error() string { return e.Msg }

func errf(format string, a ...any) error { return Error{fmt.Sprintf(format, a...)} }

type engine struct {
	p     Product
	rules []Rule
	ctx   Context
	sel   Selection
}

// Price quotes a selection for a product.
func Price(p Product, rules []Rule, sel Selection, ctx Context) (*Quote, error) {
	if sel.Kind == "" {
		sel.Kind = "new"
	}
	e := &engine{p: p, rules: rules, ctx: ctx, sel: sel}
	q := &Quote{Lines: []Line{}, Adjustments: []Line{}}

	switch sel.Kind {
	case "new", "renew":
		if err := e.priceItems(q); err != nil {
			return nil, err
		}
	case "addon":
		if err := e.priceAddon(q); err != nil {
			return nil, err
		}
	default:
		return nil, errf("نوع سفارش نامعتبر است")
	}

	for _, l := range q.Lines {
		q.Subtotal += l.Amount
	}
	total := q.Subtotal

	if sel.Kind == "renew" {
		if r := e.best(CompRenew, nil); r != nil && r.Value > 0 && r.Value != 1 {
			adj := roundI(float64(total)*r.Value) - total
			q.Adjustments = append(q.Adjustments, Line{Component: CompRenew, Label: fmt.Sprintf("تمدید (×%s)", fmtF(r.Value)), Amount: adj, RuleID: r.ID})
			total += adj
		}
	}
	if pct := ctx.GroupDiscountPct; pct > 0 && total > 0 {
		adj := -roundI(float64(total) * math.Min(pct, 100) / 100)
		q.Adjustments = append(q.Adjustments, Line{Component: "group", Label: fmt.Sprintf("تخفیف گروه کاربری (%s٪)", fmtF(pct)), Amount: adj})
		total += adj
	}
	if r := e.best(CompSale, nil); r != nil && r.Value > 0 && total > 0 {
		adj := -roundI(float64(total) * math.Min(r.Value, 100) / 100)
		label := "فروش ویژه"
		if r.Note != "" {
			label = r.Note
		}
		q.Adjustments = append(q.Adjustments, Line{Component: CompSale, Label: fmt.Sprintf("%s (%s٪)", label, fmtF(r.Value)), Amount: adj, RuleID: r.ID})
		total += adj
	}
	if c := ctx.Coupon; c != nil && total > 0 {
		if total < c.MinAmount {
			return nil, errf("حداقل مبلغ برای این کد تخفیف %d است", c.MinAmount)
		}
		var adj int64
		switch c.Kind {
		case "percent":
			adj = -roundI(float64(total) * math.Min(c.Value, 100) / 100)
		case "amount":
			adj = -int64(math.Min(c.Value, float64(total)))
		}
		q.Adjustments = append(q.Adjustments, Line{Component: "coupon", Label: "کد تخفیف " + c.Code, Amount: adj})
		total += adj
	}
	if ctx.RoundTo > 1 && total > 0 {
		rounded := roundI(float64(total)/float64(ctx.RoundTo)) * ctx.RoundTo
		if rounded != total {
			q.Adjustments = append(q.Adjustments, Line{Component: "round", Label: "گرد کردن", Amount: rounded - total})
			total = rounded
		}
	}
	if total < 0 {
		total = 0
	}
	q.Total = total
	return q, nil
}

// --- items ---

func (e *engine) priceItems(q *Quote) error {
	o := e.p.Options
	m := e.sel.Model
	if !slices.Contains(o.Models, m) {
		return errf("این روش فروش برای این محصول فعال نیست")
	}
	if err := e.checkChoices(); err != nil {
		return err
	}
	switch m {
	case ModelPackage:
		return e.pricePackage(q)
	case ModelPayg:
		return e.pricePayg(q)
	}

	s := Spec{Model: m, Days: e.sel.Days, RateMbit: e.sel.RateMbit, MaxIPs: e.sel.MaxIPs}
	if m == ModelPerGB {
		s.Days = o.PerGBDays
		if s.Days == 0 {
			s.Days = 365
		}
	} else if err := e.checkDays(s.Days); err != nil {
		return err
	}

	if e.p.Type == "port" {
		n := len(e.sel.Ports)
		if err := between("تعداد پورت", n, max(o.MinPorts, 1), o.MaxPorts); err != nil {
			return err
		}
		s.Ports = n
		if m != ModelPerGB {
			if err := e.portLines(q); err != nil {
				return err
			}
		}
	}

	switch m {
	case ModelBuilder, ModelPerGB:
		if e.p.Type == "outbound" {
			if err := e.locationLines(q, &s, true); err != nil {
				return err
			}
		} else {
			if err := e.checkGB(e.sel.GB, m == ModelPerGB); err != nil {
				return err
			}
			s.GB = e.sel.GB
			if err := e.gbLine(q, e.sel.GB, nil, "حجم"); err != nil {
				return err
			}
		}
	case ModelPerPort:
		s.GB = o.PerPortGB * s.Ports
		if e.p.Type == "outbound" {
			if err := e.locationLines(q, &s, false); err != nil {
				return err
			}
		}
	case ModelUnlimited:
		s.GB = 0
		if s.RateMbit <= 0 {
			return errf("برای حجم نامحدود باید سقف سرعت انتخاب شود")
		}
		if e.p.Type == "outbound" {
			if err := e.locationLines(q, &s, false); err != nil {
				return err
			}
		}
	}

	if s.RateMbit > 0 || o.MinRate > 0 {
		if err := between("سقف سرعت", s.RateMbit, o.MinRate, o.MaxRate); err != nil {
			return err
		}
		if r := e.best(CompRate, nil); r != nil && s.RateMbit > 0 {
			amt := graduated(r, s.RateMbit)
			q.Lines = append(q.Lines, Line{Component: CompRate, Label: fmt.Sprintf("سقف سرعت %d Mbps", s.RateMbit), Qty: float64(s.RateMbit), Unit: unitOf(amt, s.RateMbit), Amount: amt, RuleID: r.ID, timed: true})
		}
	}
	if s.MaxIPs > 0 {
		if o.MaxIPs > 0 && s.MaxIPs > o.MaxIPs {
			return errf("حداکثر IP هم‌زمان %d است", o.MaxIPs)
		}
		if extra := s.MaxIPs - o.IncludedIPs; extra > 0 {
			r := e.best(CompIP, nil)
			if r == nil {
				return errf("قیمت IP اضافه تعریف نشده است")
			}
			amt := graduated(r, extra)
			q.Lines = append(q.Lines, Line{Component: CompIP, Label: fmt.Sprintf("%d IP اضافه", extra), Qty: float64(extra), Unit: unitOf(amt, extra), Amount: amt, RuleID: r.ID, timed: true})
		}
	} else {
		s.MaxIPs = o.IncludedIPs
	}
	if r := e.best(CompBase, nil); r != nil && r.Value != 0 {
		q.Lines = append(q.Lines, Line{Component: CompBase, Label: "هزینه‌ی پایه", Qty: 1, Unit: roundI(r.Value), Amount: roundI(r.Value), RuleID: r.ID, timed: m != ModelPerGB})
	}

	if m != ModelPerGB {
		e.applyDuration(q, s.Days)
	}
	q.Spec = s
	return nil
}

func (e *engine) pricePackage(q *Quote) error {
	var pkg *Package
	for i := range e.p.Options.Packages {
		if e.p.Options.Packages[i].ID == e.sel.PackageID {
			pkg = &e.p.Options.Packages[i]
		}
	}
	if pkg == nil {
		return errf("بسته انتخاب‌شده وجود ندارد")
	}
	price := pkg.Price
	var ruleID int64
	if r := e.best(CompPackage, map[string]string{"package_id": pkg.ID}); r != nil {
		price, ruleID = roundI(r.Value), r.ID
	}
	if e.p.Type == "port" && len(e.sel.Ports) > 0 && len(e.sel.Ports) != pkg.Ports {
		return errf("این بسته %d پورت دارد", pkg.Ports)
	}
	q.Lines = append(q.Lines, Line{Component: CompPackage, Label: "بسته‌ی " + pkg.Name, Qty: 1, Unit: price, Amount: price, RuleID: ruleID})
	q.Spec = Spec{Model: ModelPackage, Ports: pkg.Ports, GB: pkg.GB, Days: pkg.Days, RateMbit: pkg.RateMbit, MaxIPs: pkg.MaxIPs, Locations: pkg.Locations}
	return nil
}

func (e *engine) pricePayg(q *Quote) error {
	o := e.p.Options
	gb, day := e.best(CompPaygGB, nil), e.best(CompPaygDay, nil)
	if gb == nil && day == nil {
		return errf("نرخ پرداخت به‌ازای مصرف تعریف نشده است")
	}
	if gb != nil {
		q.PaygPerGB = roundI(gb.Value)
	}
	if day != nil {
		q.PaygPerDay = roundI(day.Value)
	}
	q.PaygMinimum = o.PaygMinTopup
	q.Spec = Spec{Model: ModelPayg, Ports: len(e.sel.Ports), RateMbit: e.sel.RateMbit, MaxIPs: max(e.sel.MaxIPs, o.IncludedIPs)}
	return nil
}

func (e *engine) portLines(q *Quote) error {
	// Group ports by class so each class gets one line.
	counts := map[int64]int{}
	order := []int64{}
	for _, ps := range e.sel.Ports {
		if counts[ps.ClassID] == 0 {
			order = append(order, ps.ClassID)
		}
		counts[ps.ClassID]++
	}
	for _, cls := range order {
		n := counts[cls]
		r := e.best(CompPort, map[string]string{"class_id": strconv.FormatInt(cls, 10)})
		if r == nil {
			return errf("قیمت این نوع پورت تعریف نشده است")
		}
		amt := graduated(r, n)
		label := "پورت"
		if name := e.ctx.ClassNames[cls]; name != "" {
			label = "پورت " + name
		}
		q.Lines = append(q.Lines, Line{Component: CompPort, Label: label, Qty: float64(n), Unit: unitOf(amt, n), Amount: amt, RuleID: r.ID, timed: true})
	}
	return nil
}

func (e *engine) locationLines(q *Quote, s *Spec, chargeGB bool) error {
	o := e.p.Options
	if len(e.sel.Locations) == 0 {
		return errf("حداقل یک لوکیشن انتخاب کنید")
	}
	if len(e.sel.Locations) < o.MinLocations {
		return errf("حداقل %d لوکیشن انتخاب کنید", o.MinLocations)
	}
	seen := map[int64]bool{}
	for _, l := range e.sel.Locations {
		if seen[l.LocationID] {
			return errf("لوکیشن تکراری است")
		}
		seen[l.LocationID] = true
		if len(o.Locations) > 0 && !slices.Contains(o.Locations, l.LocationID) {
			return errf("این لوکیشن برای این محصول فعال نیست")
		}
		name := e.ctx.LocationNames[l.LocationID]
		if name == "" {
			name = fmt.Sprintf("#%d", l.LocationID)
		}
		scope := map[string]string{"location_id": strconv.FormatInt(l.LocationID, 10)}
		if r := e.best(CompLocation, scope); r != nil && r.Value != 0 {
			q.Lines = append(q.Lines, Line{Component: CompLocation, Label: "لوکیشن " + name, Qty: 1, Unit: roundI(r.Value), Amount: roundI(r.Value), RuleID: r.ID, timed: true})
		}
		gb := l.GB
		if chargeGB {
			if err := e.checkGB(gb, false); err != nil {
				return err
			}
			if err := e.gbLine(q, gb, scope, "حجم "+name); err != nil {
				return err
			}
		} else {
			gb = 0
			if e.sel.Model == ModelPerPort {
				gb = o.PerPortGB
			}
		}
		s.Locations = append(s.Locations, LocSel{LocationID: l.LocationID, GB: gb})
	}
	return nil
}

func (e *engine) gbLine(q *Quote, gb int, scope map[string]string, label string) error {
	if gb <= 0 {
		return nil
	}
	r := e.best(CompGB, scope)
	if r == nil {
		return errf("قیمت حجم تعریف نشده است")
	}
	amt := graduated(r, gb)
	q.Lines = append(q.Lines, Line{Component: CompGB, Label: fmt.Sprintf("%s %d GB", label, gb), Qty: float64(gb), Unit: unitOf(amt, gb), Amount: amt, RuleID: r.ID})
	return nil
}

func (e *engine) applyDuration(q *Quote, days int) {
	var timed int64
	for _, l := range q.Lines {
		if l.timed {
			timed += l.Amount
		}
	}
	if timed == 0 {
		return
	}
	factor := float64(days) / 30
	var ruleID int64
	if r := e.best(CompDuration, nil); r != nil {
		for _, t := range r.Tiers {
			if t.Days == days && t.Mult > 0 {
				factor, ruleID = t.Mult, r.ID
			}
		}
	}
	if factor == 1 {
		return
	}
	adj := roundI(float64(timed)*factor) - timed
	q.Lines = append(q.Lines, Line{Component: CompDuration, Label: fmt.Sprintf("مدت %d روز (×%s)", days, fmtF(factor)), Qty: factor, Amount: adj, RuleID: ruleID})
}

func (e *engine) priceAddon(q *Quote) error {
	k, n := e.sel.AddonKind, e.sel.AddonQty
	if n <= 0 {
		return errf("مقدار افزودنی را وارد کنید")
	}
	labels := map[string]string{"gb": "حجم اضافه (GB)", "days": "روز اضافه", "port": "پورت اضافه", "location": "لوکیشن اضافه", "rate": "سرعت اضافه (Mbps)", "ip": "IP اضافه"}
	label, ok := labels[k]
	if !ok {
		return errf("نوع افزودنی نامعتبر است")
	}
	scope := map[string]string{"addon_kind": k}
	if e.sel.Locations != nil && len(e.sel.Locations) == 1 {
		scope["location_id"] = strconv.FormatInt(e.sel.Locations[0].LocationID, 10)
	}
	r := e.best(CompAddon, scope)
	if r == nil && k == "gb" { // fall back to the normal GB price
		var gbScope map[string]string
		if lid, ok := scope["location_id"]; ok {
			gbScope = map[string]string{"location_id": lid}
		}
		r = e.best(CompGB, gbScope)
	}
	if r == nil {
		return errf("قیمت این افزودنی تعریف نشده است")
	}
	amt := graduated(r, n)
	q.Lines = append(q.Lines, Line{Component: CompAddon, Label: label, Qty: float64(n), Unit: unitOf(amt, n), Amount: amt, RuleID: r.ID})
	q.Spec = Spec{Model: e.sel.Model}
	return nil
}

// --- validation ---

func (e *engine) checkChoices() error {
	o := e.p.Options
	if e.sel.Core != "" && len(o.Cores) > 0 && !slices.Contains(o.Cores, e.sel.Core) {
		return errf("این نوع تانل برای این محصول فعال نیست")
	}
	if e.sel.Proto != "" && len(o.Protos) > 0 && !slices.Contains(o.Protos, e.sel.Proto) {
		return errf("این پروتکل برای این محصول فعال نیست")
	}
	if e.sel.Delivery != "" && len(o.Deliveries) > 0 && !slices.Contains(o.Deliveries, e.sel.Delivery) {
		return errf("این حالت تحویل برای این محصول فعال نیست")
	}
	return nil
}

func (e *engine) checkDays(d int) error {
	o := e.p.Options
	if d <= 0 {
		return errf("مدت را انتخاب کنید")
	}
	if len(o.Durations) > 0 {
		if !slices.Contains(o.Durations, d) {
			return errf("این مدت قابل انتخاب نیست")
		}
		return nil
	}
	return between("مدت (روز)", d, o.MinDays, o.MaxDays)
}

func (e *engine) checkGB(gb int, required bool) error {
	o := e.p.Options
	if gb <= 0 {
		if required || o.MinGB > 0 {
			return errf("حجم را انتخاب کنید")
		}
		return errf("حجم نامحدود در این روش فروش ممکن نیست")
	}
	if o.GBStep > 1 && gb%o.GBStep != 0 {
		return errf("حجم باید مضرب %d گیگ باشد", o.GBStep)
	}
	return between("حجم (GB)", gb, o.MinGB, o.MaxGB)
}

func between(what string, v, lo, hi int) error {
	if lo > 0 && v < lo {
		return errf("%s حداقل %d است", what, lo)
	}
	if hi > 0 && v > hi {
		return errf("%s حداکثر %d است", what, hi)
	}
	return nil
}

// --- rule selection ---

// best returns the most specific enabled rule for a component. A rule
// matches when every key in its scope equals the lookup's key, and its
// product/group/user (when set) match the order. Specificity: user > group >
// product > number of scope keys > priority > newest.
func (e *engine) best(comp string, keys map[string]string) *Rule {
	lookup := map[string]string{}
	for k, v := range keys {
		lookup[k] = v
	}
	s := e.sel
	if s.Core != "" {
		lookup["core"] = s.Core
	}
	if s.Proto != "" {
		lookup["proto"] = s.Proto
	}
	if s.Delivery != "" {
		lookup["delivery"] = s.Delivery
	}
	if s.NodeID > 0 {
		lookup["node_id"] = strconv.FormatInt(s.NodeID, 10)
	}
	if s.Model != "" {
		lookup["model"] = string(s.Model)
	}

	var cands []*Rule
	for i := range e.rules {
		r := &e.rules[i]
		if r.Component != comp || !r.Enabled {
			continue
		}
		if r.ProductID != 0 && r.ProductID != e.p.ID {
			continue
		}
		if r.GroupID != 0 && r.GroupID != e.ctx.GroupID {
			continue
		}
		if r.UserID != 0 && r.UserID != e.ctx.UserID {
			continue
		}
		if r.ValidFrom > 0 && e.ctx.Now < r.ValidFrom || r.ValidTo > 0 && e.ctx.Now >= r.ValidTo {
			continue
		}
		ok := true
		for k, v := range r.Scope {
			if v == "" {
				continue
			}
			if lookup[k] != v {
				ok = false
				break
			}
		}
		if ok {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if sa, sb := specificity(a), specificity(b); sa != sb {
			return sa > sb
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return a.ID > b.ID
	})
	return cands[0]
}

func specificity(r *Rule) int {
	n := 0
	if r.UserID != 0 {
		n += 1000
	}
	if r.GroupID != 0 {
		n += 100
	}
	if r.ProductID != 0 {
		n += 10
	}
	for _, v := range r.Scope {
		if v != "" {
			n++
		}
	}
	return n
}

// graduated prices n units: each band of tiers at its own price; without
// tiers, n × value.
func graduated(r *Rule, n int) int64 {
	if len(r.Tiers) == 0 {
		return roundI(float64(n) * r.Value)
	}
	tiers := append([]Tier(nil), r.Tiers...)
	sort.SliceStable(tiers, func(i, j int) bool {
		if tiers[i].Upto == 0 {
			return false
		}
		if tiers[j].Upto == 0 {
			return true
		}
		return tiers[i].Upto < tiers[j].Upto
	})
	var total float64
	prev := 0
	for _, t := range tiers {
		cap := t.Upto
		if cap == 0 || cap > n {
			cap = n
		}
		if cap > prev {
			total += float64(cap-prev) * t.Price
			prev = cap
		}
		if prev >= n {
			break
		}
	}
	if prev < n { // units beyond the last bounded tier: last tier's price
		total += float64(n-prev) * tiers[len(tiers)-1].Price
	}
	return roundI(total)
}

func unitOf(amount int64, n int) int64 {
	if n <= 0 {
		return 0
	}
	return roundI(float64(amount) / float64(n))
}

func roundI(f float64) int64 { return int64(math.Round(f)) }

func fmtF(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ValidateRule checks an admin-written rule.
func ValidateRule(r Rule) error {
	if !slices.Contains(AllComponents, r.Component) {
		return errors.New("جزء قیمت ناشناخته است")
	}
	if r.Value < 0 {
		return errors.New("مقدار نمی‌تواند منفی باشد")
	}
	switch r.Component {
	case CompSale:
		if r.Value > 100 {
			return errors.New("درصد فروش ویژه حداکثر ۱۰۰ است")
		}
	case CompDuration:
		if len(r.Tiers) == 0 {
			return errors.New("برای ضریب مدت حداقل یک ردیف {روز، ضریب} لازم است")
		}
		for _, t := range r.Tiers {
			if t.Days <= 0 || t.Mult <= 0 {
				return errors.New("هر ردیف مدت باید روز و ضریب مثبت داشته باشد")
			}
		}
	default:
		for _, t := range r.Tiers {
			if t.Price < 0 || t.Upto < 0 {
				return errors.New("پله‌های قیمت نامعتبر است")
			}
		}
	}
	if r.ValidTo > 0 && r.ValidFrom > 0 && r.ValidTo <= r.ValidFrom {
		return errors.New("بازه‌ی زمانی نامعتبر است")
	}
	return nil
}
