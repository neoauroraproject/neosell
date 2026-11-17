package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"neosell/internal/catalog"
	"neosell/internal/db"
	"neosell/internal/jobs"
	"neosell/internal/license"
	"neosell/internal/pricing"
	"neosell/internal/wallet"
)

type OrderRequest struct {
	Kind           string            `json:"kind"` // new | renew | addon
	ProductID      int64             `json:"product_id"`
	ServiceID      int64             `json:"service_id"`
	Selection      pricing.Selection `json:"selection"`
	IdempotencyKey string            `json:"idempotency_key"`
}

type Order struct {
	ID        int64          `json:"id"`
	UserID    int64          `json:"user_id"`
	UserEmail string         `json:"user_email,omitempty"`
	Kind      string         `json:"kind"`
	ProductID int64          `json:"product_id"`
	ServiceID int64          `json:"service_id"`
	Amount    int64          `json:"amount"`
	Status    string         `json:"status"`
	Quote     *pricing.Quote `json:"quote,omitempty"`
	CreatedAt int64          `json:"created_at"`
}

// OrderError is a customer-facing refusal (bad selection, no stock, …).
type OrderError struct{ Msg string }

func (e OrderError) Error() string { return e.Msg }

func oerr(format string, a ...any) error { return OrderError{fmt.Sprintf(format, a...)} }

func productModule(t string) string {
	if t == "outbound" {
		return license.ModOutbound
	}
	return license.ModPorts
}

func (m *Manager) userGroup(ctx context.Context, uid int64) int64 {
	var g int64
	m.DB.QueryRowContext(ctx, `SELECT COALESCE(group_id,0) FROM users WHERE id = ?`, uid).Scan(&g)
	return g
}

func (m *Manager) couponUsage(ctx context.Context, uid int64, code string) catalog.CouponUsage {
	var u catalog.CouponUsage
	m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM orders o JOIN discounts d ON d.id = o.discount_id WHERE o.user_id = ? AND d.code = ? AND o.status = 'paid'`,
		uid, strings.ToUpper(strings.TrimSpace(code))).Scan(&u.ByUser)
	var n int
	m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM orders WHERE user_id = ? AND status = 'paid'`, uid).Scan(&n)
	u.HasOrderedOK = n > 0
	return u
}

// Quote prices an order exactly as PlaceOrder would.
func (m *Manager) Quote(ctx context.Context, userID int64, req *OrderRequest) (*catalog.Product, *pricing.Quote, error) {
	sel := req.Selection
	switch req.Kind {
	case "", "new":
		req.Kind = "new"
		sel.Kind = "new"
	case "renew", "addon":
		svc, err := m.service(ctx, req.ServiceID)
		if err != nil || svc.UserID != userID || !liveStatus(svc.Status) {
			return nil, nil, oerr("سرویس پیدا نشد")
		}
		req.ProductID = svc.ProductID
		if req.Kind == "renew" {
			coupon := sel.Coupon
			json.Unmarshal([]byte(svc.selectionRaw), &sel)
			sel.Coupon = coupon
			sel.Kind = "renew"
		} else {
			sel.Kind = "addon"
			sel.Model = pricing.Model(svc.Model)
		}
	default:
		return nil, nil, oerr("نوع سفارش نامعتبر است")
	}
	in := &catalog.QuoteInput{ProductID: req.ProductID, Selection: sel}
	p, rules, pctx, err := m.Catalog.Prepare(ctx, in, catalog.Customer{UserID: userID, GroupID: m.userGroup(ctx, userID)},
		m.now(), m.Settings.Int(ctx, "pricing.round_to"), m.couponUsage(ctx, userID, sel.Coupon))
	if err != nil {
		return nil, nil, err
	}
	q, err := pricing.Price(p.Pricing(), rules, in.Selection, pctx)
	if err != nil {
		return nil, nil, err
	}
	req.Selection = in.Selection // ports now carry server-assigned classes
	return p, q, nil
}

// PlaceOrder charges the wallet and creates/changes the service atomically.
// A retried request with the same idempotency key returns the first order.
func (m *Manager) PlaceOrder(ctx context.Context, userID int64, req OrderRequest) (*Order, error) {
	if req.IdempotencyKey != "" {
		if o, err := m.orderByKey(ctx, userID, req.IdempotencyKey); err == nil {
			return o, nil
		}
	}
	if err := m.License.RequireActive(); err != nil {
		return nil, err
	}
	p, q, err := m.Quote(ctx, userID, &req)
	if err != nil {
		return nil, err
	}
	if err := m.License.RequireModule(productModule(p.Type)); err != nil {
		return nil, err
	}
	if req.Kind == "new" {
		if !p.VisibleTo(m.userGroup(ctx, userID)) {
			return nil, oerr("این محصول قابل خرید نیست")
		}
		var live int64
		m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM services WHERE status NOT IN ('deleted','failed')`).Scan(&live)
		if err := m.License.RequireBelow(license.LimActiveServices, live); err != nil {
			return nil, err
		}
	}
	sel := req.Selection
	if sel.Model == pricing.ModelPayg && req.Kind == "new" {
		if err := m.License.RequireModule(license.ModPayg); err != nil {
			return nil, err
		}
		bal, _ := m.Wallet.Balance(ctx, userID)
		if bal < q.PaygMinimum || bal <= 0 {
			return nil, oerr("برای پرداخت به‌ازای مصرف، موجودی کیف پول باید حداقل %d باشد", max(q.PaygMinimum, 1))
		}
	}

	var plan *portPlan
	if req.Kind == "new" && p.Type == "port" {
		if plan, err = m.planPorts(ctx, p, sel, q.Spec); err != nil {
			return nil, err
		}
	}
	if req.Kind == "addon" && sel.AddonKind == "port" {
		svc, _ := m.service(ctx, req.ServiceID)
		if svc.Type != "port" {
			return nil, oerr("پورت اضافه فقط برای سرویس پورت است")
		}
		ports := make([]pricing.PortSel, 0, sel.AddonQty)
		for i := 0; i < sel.AddonQty; i++ {
			ps := pricing.PortSel{}
			if i < len(sel.Ports) {
				ps = sel.Ports[i]
			}
			ports = append(ports, ps)
		}
		plan = &portPlan{NodeID: svc.NodeID, Proto: svc.Proto}
		if plan.Ports, err = m.allocate(ctx, svc.NodeID, ports, svc.Proto); err != nil {
			return nil, err
		}
	}
	var outLocs []pricing.LocSel
	if p.Type == "outbound" && (req.Kind == "new" || req.Kind == "addon" && sel.AddonKind == "location") {
		outLocs = q.Spec.Locations
		if req.Kind == "addon" {
			outLocs = sel.Locations
		}
		for _, l := range outLocs {
			var n int
			m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM outbound_templates t JOIN nodes n ON n.id = t.node_id WHERE t.location_id = ? AND t.enabled = 1 AND n.enabled = 1`, l.LocationID).Scan(&n)
			if n == 0 {
				return nil, oerr("این لوکیشن فعلاً ظرفیت ندارد")
			}
		}
	}

	var discountID any
	if sel.Coupon != "" {
		_, d, _ := m.Catalog.ResolveCoupon(ctx, sel.Coupon, p.ID, m.now(), m.couponUsage(ctx, userID, sel.Coupon))
		if d != nil {
			discountID = d.ID
		}
	}
	gb := m.gbBase(ctx)
	now := m.now()
	var orderID, serviceID int64
	pushNode := int64(0)
	syncOutbound := false
	resetOutbound := false

	err = m.DB.Tx(ctx, func(tx *sql.Tx) error {
		if req.Kind == "new" {
			res, err := tx.ExecContext(ctx, `UPDATE products SET sold = sold + 1 WHERE id = ? AND (stock < 0 OR sold < stock)`, p.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return oerr("موجودی این محصول تمام شده است")
			}
		}
		qraw, _ := json.Marshal(q)
		sraw, _ := json.Marshal(sel)
		var key any
		if req.IdempotencyKey != "" {
			key = fmt.Sprintf("%d:%s", userID, req.IdempotencyKey)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO orders(user_id, kind, product_id, service_id, selection, quote, amount, discount_id, status, idempotency_key, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'paid', ?, ?)`, userID, req.Kind, p.ID, req.ServiceID, string(sraw), string(qraw), q.Total, discountID, key, now)
		if err != nil {
			return err
		}
		orderID, _ = res.LastInsertId()
		if q.Total > 0 {
			if _, err := wallet.ApplyTx(ctx, tx, wallet.Entry{UserID: userID, Amount: -q.Total, Type: "purchase", RefType: "order",
				RefID: strconv.FormatInt(orderID, 10), Note: orderNote(req.Kind, p.Title), IdempotencyKey: fmt.Sprintf("order:%d", orderID)}); err != nil {
				return err
			}
		}
		if discountID != nil {
			tx.ExecContext(ctx, `UPDATE discounts SET used = used + 1 WHERE id = ?`, discountID)
		}

		switch req.Kind {
		case "new":
			serviceID, err = m.createService(ctx, tx, userID, p, sel, q, plan, gb, now)
			if err != nil {
				return err
			}
			if p.Type == "port" {
				pushNode = plan.NodeID
			} else {
				syncOutbound = true
			}
		case "renew":
			serviceID = req.ServiceID
			svc, err := m.serviceTx(ctx, tx, serviceID)
			if err != nil {
				return err
			}
			if err := m.renewTx(ctx, tx, svc, q.Spec, gb, now); err != nil {
				return err
			}
			pushNode, syncOutbound, resetOutbound = svc.NodeID, svc.Type == "outbound", true
		case "addon":
			serviceID = req.ServiceID
			svc, err := m.serviceTx(ctx, tx, serviceID)
			if err != nil {
				return err
			}
			if err := m.addonTx(ctx, tx, svc, sel, plan, outLocs, gb, now); err != nil {
				return err
			}
			pushNode, syncOutbound = svc.NodeID, svc.Type == "outbound"
		}
		_, err = tx.ExecContext(ctx, `UPDATE orders SET service_id = ? WHERE id = ?`, serviceID, orderID)
		return err
	})
	if err != nil {
		if errors.Is(err, wallet.ErrInsufficient) {
			return nil, oerr("موجودی کیف پول کافی نیست (مبلغ: %d)", q.Total)
		}
		if strings.Contains(err.Error(), "UNIQUE") && strings.Contains(err.Error(), "service_ports") {
			return nil, oerr("یکی از پورت‌ها همین الان فروخته شد؛ دوباره امتحان کنید")
		}
		return nil, err
	}
	if pushNode > 0 && p.Type == "port" {
		m.push(pushNode)
	}
	if syncOutbound {
		m.enqueueSync(ctx, serviceID, resetOutbound)
	}
	return m.Order(ctx, orderID)
}

func orderNote(kind, title string) string {
	switch kind {
	case "renew":
		return "تمدید " + title
	case "addon":
		return "افزودنی " + title
	}
	return "خرید " + title
}

func (m *Manager) orderByKey(ctx context.Context, uid int64, key string) (*Order, error) {
	var id int64
	if err := m.DB.QueryRowContext(ctx, `SELECT id FROM orders WHERE idempotency_key = ?`, fmt.Sprintf("%d:%s", uid, key)).Scan(&id); err != nil {
		return nil, err
	}
	return m.Order(ctx, id)
}

func (m *Manager) Order(ctx context.Context, id int64) (*Order, error) {
	o := &Order{}
	var q string
	err := m.DB.QueryRowContext(ctx, `SELECT o.id, o.user_id, COALESCE(u.email,''), o.kind, COALESCE(o.product_id,0), COALESCE(o.service_id,0), o.amount, o.status, o.quote, o.created_at
		FROM orders o LEFT JOIN users u ON u.id = o.user_id WHERE o.id = ?`, id).Scan(&o.ID, &o.UserID, &o.UserEmail, &o.Kind, &o.ProductID, &o.ServiceID, &o.Amount, &o.Status, &q, &o.CreatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(q), &o.Quote)
	return o, nil
}

func (m *Manager) Orders(ctx context.Context, userID, beforeID int64, limit int) ([]Order, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	q := `SELECT o.id, o.user_id, COALESCE(u.email,''), o.kind, COALESCE(o.product_id,0), COALESCE(o.service_id,0), o.amount, o.status, o.created_at
		FROM orders o LEFT JOIN users u ON u.id = o.user_id WHERE 1=1`
	args := []any{}
	if userID > 0 {
		q += ` AND o.user_id = ?`
		args = append(args, userID)
	}
	if beforeID > 0 {
		q += ` AND o.id < ?`
		args = append(args, beforeID)
	}
	rows, err := m.DB.QueryContext(ctx, q+` ORDER BY o.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.UserEmail, &o.Kind, &o.ProductID, &o.ServiceID, &o.Amount, &o.Status, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---------- port planning ----------

type plannedPort struct {
	Port, ToPort int
	ClassID      int64
}

type portPlan struct {
	NodeID     int64
	Core       string
	Driver     string
	Delivery   string
	Proto      string
	Ports      []plannedPort
	BridgePort int
	SharedName string
}

func firstOr(xs []string, d string) string {
	if len(xs) > 0 && xs[0] != "" {
		return xs[0]
	}
	return d
}

func (m *Manager) planPorts(ctx context.Context, p *catalog.Product, sel pricing.Selection, spec pricing.Spec) (*portPlan, error) {
	o := p.Options
	plan := &portPlan{
		Core:     nz(sel.Core, firstOr(o.Cores, "neotunnel")),
		Delivery: nz(sel.Delivery, firstOr(o.Deliveries, "shared")),
		Proto:    nz(sel.Proto, firstOr(o.Protos, "tcp")),
	}
	if !slices.Contains([]string{"tcp", "udp", "both"}, plan.Proto) {
		return nil, oerr("پروتکل نامعتبر است")
	}
	ct, err := m.coreTemplate(ctx, plan.Core)
	if err != nil || !ct.Enabled {
		return nil, oerr("این نوع تانل فعال نیست")
	}
	plan.Driver = ct.Driver
	if plan.Delivery == "dedicated" {
		ip := net.ParseIP(strings.TrimSpace(sel.PeerAddress))
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
			return nil, oerr("IP عمومی سرور خارج خود را درست وارد کنید")
		}
	}
	node, err := m.pickNode(ctx, o.Nodes, sel.NodeID, plan)
	if err != nil {
		return nil, err
	}
	plan.NodeID = node.ID
	if plan.Delivery == "shared" {
		if err := m.DB.QueryRowContext(ctx, `SELECT name FROM tunnels WHERE node_id = ? AND core = ? AND service_id IS NULL ORDER BY id LIMIT 1`, node.ID, plan.Core).Scan(&plan.SharedName); err != nil {
			return nil, oerr("تانل اشتراکی روی این سرور تعریف نشده است")
		}
	}
	// A package may carry no explicit ports: sell random ones.
	ports := sel.Ports
	for len(ports) < spec.Ports {
		ports = append(ports, pricing.PortSel{})
	}
	if plan.Ports, err = m.allocate(ctx, node.ID, ports, plan.Proto); err != nil {
		return nil, err
	}
	if plan.Delivery == "dedicated" && plan.Driver == "template" {
		if plan.BridgePort, err = m.allocateBridge(ctx, node, plan.Ports); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func (m *Manager) pickNode(ctx context.Context, allowed []int64, want int64, plan *portPlan) (*Node, error) {
	nodes, err := m.Nodes(ctx, "tunnel")
	if err != nil {
		return nil, err
	}
	var cands []Node
	for _, n := range nodes {
		if !n.Enabled || !n.SaleEnabled {
			continue
		}
		if len(allowed) > 0 && !slices.Contains(allowed, n.ID) {
			continue
		}
		if want > 0 && n.ID != want {
			continue
		}
		if len(n.Options.Cores) > 0 && !slices.Contains(n.Options.Cores, plan.Core) {
			continue
		}
		if n.Options.Capacity > 0 && n.Live >= n.Options.Capacity {
			continue
		}
		if plan.Delivery == "shared" {
			var c int
			m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM tunnels WHERE node_id = ? AND core = ? AND service_id IS NULL`, n.ID, plan.Core).Scan(&c)
			if c == 0 {
				continue
			}
		}
		cands = append(cands, n)
	}
	if len(cands) == 0 {
		return nil, oerr("فعلاً سروری با ظرفیت آزاد برای این محصول نیست")
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		ao, bo := a.Status == "online", b.Status == "online"
		if ao != bo {
			return ao
		}
		if a.Options.Weight != b.Options.Weight {
			return a.Options.Weight > b.Options.Weight
		}
		return a.Live < b.Live
	})
	return &cands[0], nil
}

// allocate assigns concrete ports on a node: requested ones are checked,
// empty ones get a random free port from the node's pool.
func (m *Manager) allocate(ctx context.Context, nodeID int64, sels []pricing.PortSel, proto string) ([]plannedPort, error) {
	n, err := m.Node(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	reserved, _ := parsePortSet(n.Options.ReservedPorts)
	used := map[int]bool{}
	rows, err := m.DB.QueryContext(ctx, `SELECT port FROM service_ports WHERE node_id = ? AND active = 1
		UNION SELECT bridge_port FROM tunnels WHERE node_id = ? AND bridge_port > 0`, nodeID, nodeID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p int
		rows.Scan(&p)
		used[p] = true
	}
	rows.Close()

	var out []plannedPort
	var wanted []int
	for _, s := range sels {
		if s.Port == "" {
			continue
		}
		p, err := strconv.Atoi(s.Port)
		if err != nil || p < 1 || p > 65535 {
			return nil, oerr("پورت %s نامعتبر است", s.Port)
		}
		if reserved[p] || used[p] {
			return nil, oerr("پورت %d قبلاً فروخته شده یا رزرو است", p)
		}
		wanted = append(wanted, p)
		used[p] = true
	}
	// Ask the agent whether anything else already listens on them.
	busy := map[int]bool{}
	check := func(ports []int) error {
		if m.Hub == nil || !m.Hub.Online(nodeID) || len(ports) == 0 {
			return nil
		}
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		b, err := m.Hub.CheckPorts(cctx, nodeID, ports, proto)
		if err != nil {
			return nil // agent slow: the unique index and the agent still protect us
		}
		for _, p := range b {
			busy[p] = true
		}
		return nil
	}
	check(wanted)
	for _, p := range wanted {
		if busy[p] {
			return nil, oerr("پورت %d روی سرور در حال استفاده است", p)
		}
	}

	pool, _ := parseRanges(nz(n.Options.PortRanges, "20000-60000"))
	randomFree := func() (int, error) {
		total := 0
		for _, r := range pool {
			total += r.hi - r.lo + 1
		}
		for i := 0; i < 300 && total > 0; i++ {
			k, _ := rand.Int(rand.Reader, big.NewInt(int64(total)))
			idx := int(k.Int64())
			for _, r := range pool {
				size := r.hi - r.lo + 1
				if idx < size {
					p := r.lo + idx
					if !used[p] && !reserved[p] && !busy[p] {
						return p, nil
					}
					break
				}
				idx -= size
			}
		}
		return 0, oerr("پورت آزاد روی سرور پیدا نشد")
	}
	var randomPicked []int
	for _, s := range sels {
		if s.Port != "" {
			p, _ := strconv.Atoi(s.Port)
			out = append(out, plannedPort{Port: p, ToPort: toPort(s.ToPort, p), ClassID: s.ClassID})
			continue
		}
		p, err := randomFree()
		if err != nil {
			return nil, err
		}
		used[p] = true
		randomPicked = append(randomPicked, p)
		out = append(out, plannedPort{Port: p, ToPort: toPort(s.ToPort, p), ClassID: s.ClassID})
	}
	// Re-check randomly picked ports; replace any that turn out busy.
	if len(randomPicked) > 0 {
		check(randomPicked)
		for i := range out {
			if busy[out[i].Port] {
				p, err := randomFree()
				if err != nil {
					return nil, err
				}
				used[p] = true
				same := out[i].ToPort == out[i].Port
				out[i].Port = p
				if same {
					out[i].ToPort = p
				}
			}
		}
	}
	return out, nil
}

func toPort(want, port int) int {
	if want > 0 && want <= 65535 {
		return want
	}
	return port
}

func (m *Manager) allocateBridge(ctx context.Context, n *Node, ports []plannedPort) (int, error) {
	rs, _ := parseRanges(nz(n.Options.BridgeRange, "40000-45000"))
	taken := map[int]bool{}
	for _, p := range ports {
		taken[p.Port] = true
	}
	rows, _ := m.DB.QueryContext(ctx, `SELECT bridge_port FROM tunnels WHERE node_id = ? UNION SELECT port FROM service_ports WHERE node_id = ? AND active = 1`, n.ID, n.ID)
	if rows != nil {
		for rows.Next() {
			var p int
			rows.Scan(&p)
			taken[p] = true
		}
		rows.Close()
	}
	for _, r := range rs {
		for p := r.lo; p <= r.hi; p++ {
			if !taken[p] {
				return p, nil
			}
		}
	}
	return 0, oerr("پورت آزاد برای اتصال تانل اختصاصی نیست")
}

// ---------- service creation / changes ----------

const subAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func randID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = subAlphabet[int(b[i])%len(subAlphabet)]
	}
	return string(b)
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (m *Manager) createService(ctx context.Context, tx *sql.Tx, uid int64, p *catalog.Product, sel pricing.Selection, q *pricing.Quote, plan *portPlan, gb, now int64) (int64, error) {
	spec := q.Spec
	var expires int64
	if spec.Days > 0 && spec.Model != pricing.ModelPayg {
		expires = now + int64(spec.Days)*86400
	}
	status := StProvisioning
	var nodeID any
	core, delivery, proto := "", "shared", "tcp"
	if plan != nil {
		nodeID, core, delivery, proto = plan.NodeID, plan.Core, plan.Delivery, plan.Proto
		status = StActive
		if delivery == "dedicated" && plan.Driver == "neotunnel" {
			status = StWaitingPeer
		}
	}
	specRaw, _ := json.Marshal(spec)
	sel.Coupon = "" // never re-apply a coupon on renewal
	selRaw, _ := json.Marshal(sel)
	quota := int64(spec.GB) * gb
	if p.Type == "outbound" {
		quota = 0 // per-location quotas
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO services(user_id, product_id, type, model, title, status, spec, selection, quota_bytes, period_start, expires_at,
		rate_mbit, max_ips, node_id, core, delivery, proto, sub_token, xray_uuid, payg_per_gb, payg_per_day, payg_billed_at, status_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, p.ID, p.Type, string(spec.Model), p.Title, status, string(specRaw), string(selRaw), quota, now, expires,
		spec.RateMbit, spec.MaxIPs, nodeID, core, delivery, proto, randID(24), newUUID(), q.PaygPerGB, q.PaygPerDay, now, now, now, now)
	if err != nil {
		return 0, err
	}
	sid, _ := res.LastInsertId()

	if plan != nil {
		for _, pp := range plan.Ports {
			if _, err := tx.ExecContext(ctx, `INSERT INTO service_ports(service_id, node_id, port, to_port, class_id) VALUES (?, ?, ?, ?, ?)`,
				sid, plan.NodeID, pp.Port, pp.ToPort, pp.ClassID); err != nil {
				return 0, err
			}
		}
		if delivery == "dedicated" {
			params, _ := json.Marshal(map[string]string{"peer_address": strings.TrimSpace(sel.PeerAddress)})
			if _, err := tx.ExecContext(ctx, `INSERT INTO tunnels(node_id, service_id, core, name, mode, bridge_port, secret, peer_token, peer_status, params, created_at)
				VALUES (?, ?, ?, ?, 'dedicated', ?, ?, ?, 'waiting', ?, ?)`, plan.NodeID, sid, core, fmt.Sprintf("ns%d", sid),
				plan.BridgePort, randHex(16), randHex(20), string(params), now); err != nil {
				return 0, err
			}
		}
	}
	if p.Type == "outbound" {
		for _, l := range spec.Locations {
			if err := m.addLocationTx(ctx, tx, sid, l, gb, expires); err != nil {
				return 0, err
			}
		}
	}
	return sid, nil
}

func (m *Manager) addLocationTx(ctx context.Context, tx *sql.Tx, sid int64, l pricing.LocSel, gb, expires int64) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO service_locations(service_id, location_id, sub_id, quota_bytes, expires_at, status) VALUES (?, ?, ?, ?, ?, 'provisioning')`,
		sid, l.LocationID, randID(16), int64(l.GB)*gb, expires)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return oerr("این لوکیشن قبلاً در سرویس هست")
		}
		return err
	}
	lid, _ := res.LastInsertId()
	rows, err := tx.QueryContext(ctx, `SELECT t.id FROM outbound_templates t JOIN nodes n ON n.id = t.node_id WHERE t.location_id = ? AND t.enabled = 1 AND n.enabled = 1`, l.LocationID)
	if err != nil {
		return err
	}
	var tids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		tids = append(tids, id)
	}
	rows.Close()
	var uuid string
	tx.QueryRowContext(ctx, `SELECT xray_uuid FROM services WHERE id = ?`, sid).Scan(&uuid)
	for _, tid := range tids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_location_clients(service_location_id, template_id, email, uuid) VALUES (?, ?, ?, ?)`,
			lid, tid, fmt.Sprintf("ns%d-%d-%d", sid, l.LocationID, tid), uuid); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) renewTx(ctx context.Context, tx *sql.Tx, svc *svcRow, spec pricing.Spec, gb, now int64) error {
	if svc.Status == StWaitingPeer || svc.Status == StProvisioning {
		return oerr("سرویس هنوز راه‌اندازی نشده است")
	}
	days := int64(spec.Days)
	base := svc.ExpiresAt
	if base < now {
		base = now
	}
	expires := base + days*86400
	quota := int64(spec.GB) * gb
	if svc.Type == "outbound" {
		quota = 0
	}
	_, err := tx.ExecContext(ctx, `UPDATE services SET status = 'active', status_reason = '', status_at = ?, expires_at = ?, period_start = ?,
		quota_bytes = ?, used_up = 0, used_down = 0, notified = '{}', updated_at = ? WHERE id = ?`, now, expires, now, quota, now, svc.ID)
	if err != nil {
		return err
	}
	if svc.Type == "outbound" {
		// Each location gets its package quota again (per location GB from the spec).
		perLoc := map[int64]int{}
		for _, l := range spec.Locations {
			perLoc[l.LocationID] = l.GB
		}
		rows, _ := tx.QueryContext(ctx, `SELECT id, location_id, quota_bytes FROM service_locations WHERE service_id = ?`, svc.ID)
		type loc struct{ id, lid, q int64 }
		var ls []loc
		for rows.Next() {
			var l loc
			rows.Scan(&l.id, &l.lid, &l.q)
			ls = append(ls, l)
		}
		rows.Close()
		for _, l := range ls {
			q := l.q
			if g, ok := perLoc[l.lid]; ok {
				q = int64(g) * gb
			}
			if _, err := tx.ExecContext(ctx, `UPDATE service_locations SET quota_bytes = ?, used_up = 0, used_down = 0, expires_at = ?, status = 'active' WHERE id = ?`, q, expires, l.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) addonTx(ctx context.Context, tx *sql.Tx, svc *svcRow, sel pricing.Selection, plan *portPlan, locs []pricing.LocSel, gb, now int64) error {
	n := int64(sel.AddonQty)
	switch sel.AddonKind {
	case "gb":
		if svc.Type == "outbound" {
			if len(sel.Locations) != 1 {
				return oerr("لوکیشنی که حجم به آن اضافه می‌شود را انتخاب کنید")
			}
			res, err := tx.ExecContext(ctx, `UPDATE service_locations SET quota_bytes = quota_bytes + ?, status = CASE WHEN status = 'limited' THEN 'active' ELSE status END
				WHERE service_id = ? AND location_id = ? AND quota_bytes > 0`, n*gb, svc.ID, sel.Locations[0].LocationID)
			if err != nil {
				return err
			}
			if c, _ := res.RowsAffected(); c == 0 {
				return oerr("این لوکیشن در سرویس نیست یا حجمش نامحدود است")
			}
			_, err = tx.ExecContext(ctx, `UPDATE services SET status = 'active', status_at = ?, updated_at = ? WHERE id = ? AND status = 'limited'`, now, now, svc.ID)
			return err
		}
		if svc.QuotaBytes == 0 {
			return oerr("حجم این سرویس نامحدود است")
		}
		_, err := tx.ExecContext(ctx, `UPDATE services SET quota_bytes = quota_bytes + ?, status = CASE WHEN status = 'limited' THEN 'active' ELSE status END,
			notified = '{}', status_at = ?, updated_at = ? WHERE id = ?`, n*gb, now, now, svc.ID)
		return err
	case "days":
		base := svc.ExpiresAt
		if base == 0 {
			return oerr("این سرویس تاریخ انقضا ندارد")
		}
		if base < now {
			base = now
		}
		_, err := tx.ExecContext(ctx, `UPDATE services SET expires_at = ?, status = CASE WHEN status = 'expired' THEN 'active' ELSE status END,
			status_at = ?, updated_at = ? WHERE id = ?`, base+n*86400, now, now, svc.ID)
		if err == nil && svc.Type == "outbound" {
			_, err = tx.ExecContext(ctx, `UPDATE service_locations SET expires_at = ?, status = CASE WHEN status = 'expired' THEN 'active' ELSE status END WHERE service_id = ?`, base+n*86400, svc.ID)
		}
		return err
	case "rate":
		_, err := tx.ExecContext(ctx, `UPDATE services SET rate_mbit = rate_mbit + ?, updated_at = ? WHERE id = ? AND rate_mbit > 0`, n, now, svc.ID)
		return err
	case "ip":
		_, err := tx.ExecContext(ctx, `UPDATE services SET max_ips = max_ips + ?, updated_at = ? WHERE id = ?`, n, now, svc.ID)
		return err
	case "port":
		for _, pp := range plan.Ports {
			if _, err := tx.ExecContext(ctx, `INSERT INTO service_ports(service_id, node_id, port, to_port, class_id) VALUES (?, ?, ?, ?, ?)`,
				svc.ID, svc.NodeID, pp.Port, pp.ToPort, pp.ClassID); err != nil {
				return err
			}
		}
		return nil
	case "location":
		for _, l := range locs {
			if err := m.addLocationTx(ctx, tx, svc.ID, l, gb, svc.ExpiresAt); err != nil {
				return err
			}
		}
		return nil
	}
	return oerr("نوع افزودنی نامعتبر است")
}

// Refund returns an order's amount to the wallet once.
func (m *Manager) Refund(ctx context.Context, orderID int64, reason string) error {
	return m.DB.Tx(ctx, func(tx *sql.Tx) error {
		var uid, amount int64
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT user_id, amount, status FROM orders WHERE id = ?`, orderID).Scan(&uid, &amount, &status); err != nil {
			return err
		}
		if status != "paid" {
			return nil
		}
		if amount > 0 {
			if _, err := wallet.ApplyTx(ctx, tx, wallet.Entry{UserID: uid, Amount: amount, Type: "refund", RefType: "order",
				RefID: strconv.FormatInt(orderID, 10), Note: reason, IdempotencyKey: fmt.Sprintf("refund:%d", orderID)}); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE orders SET status = 'refunded', refunded_at = ? WHERE id = ?`, db.Now(), orderID)
		return err
	})
}

var _ = jobs.Permanent
