package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"

	"neosell/internal/license"
)

func (c *client) upload(path string, content []byte) (int, map[string]any) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "r.png")
	fw.Write(content)
	mw.WriteField("note", "paid")
	mw.Close()
	req, _ := http.NewRequest("POST", c.e.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Requested-With", "neosell")
	resp, err := c.c.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func (c *client) must(method, path string, body any) map[string]any {
	c.e.t.Helper()
	code, out := c.req(method, path, body)
	if code != 200 {
		c.e.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (c *client) mustUpload(path string, content []byte) map[string]any {
	c.e.t.Helper()
	code, out := c.upload(path, content)
	if code != 200 {
		c.e.t.Fatalf("upload %s: %d %v", path, code, out)
	}
	return out
}

func TestShopPricingAndWalletFlow(t *testing.T) {
	e := newEnv(t, []string{license.ModPorts}, nil)
	owner := e.client()
	owner.req("POST", "/api/setup", map[string]string{"email": "o@x.io", "password": "password1"})
	owner.req("POST", "/api/admin/license/activate", map[string]string{"key": "HM-S"})

	// Catalog: port classes, a group, a product, rules.
	premium := num(owner.must("POST", "/api/admin/port-classes", map[string]any{"name": "ویژه", "kind": "specific", "expr": "443, 8443"})["id"])
	owner.must("POST", "/api/admin/port-classes", map[string]any{"name": "عادی", "kind": "range", "expr": "20000-30000"})
	if code, _ := owner.req("POST", "/api/admin/port-classes", map[string]any{"name": "bad", "kind": "range", "expr": "9-1"}); code != 400 {
		t.Fatalf("bad range accepted: %d", code)
	}
	vip := num(owner.must("POST", "/api/admin/groups", map[string]any{"name": "VIP", "discount_pct": 10})["id"])
	prod := num(owner.must("POST", "/api/admin/products", map[string]any{"type": "port", "title": "Tehran", "enabled": true, "stock": -1,
		"options": map[string]any{"models": []string{"builder"}, "durations": []int{30, 90}, "max_ports": 3, "min_gb": 10}})["id"])
	if code, _ := owner.req("POST", "/api/admin/products", map[string]any{"type": "port", "title": "x", "options": map[string]any{"models": []string{}}}); code != 400 {
		t.Fatalf("product without models accepted: %d", code)
	}
	for _, rule := range []map[string]any{
		{"component": "port", "value": 80000, "enabled": true},
		{"component": "port", "scope": map[string]string{"class_id": jsonID(premium)}, "value": 150000, "enabled": true},
		{"component": "gb", "value": 2000, "enabled": true},
		{"component": "duration", "tiers": []map[string]any{{"days": 90, "mult": 2.7}}, "enabled": true},
	} {
		owner.must("POST", "/api/admin/price-rules", rule)
	}
	if code, _ := owner.req("POST", "/api/admin/price-rules", map[string]any{"component": "sale", "value": 300, "enabled": true}); code != 400 {
		t.Fatalf("bad rule accepted: %d", code)
	}

	// Customer quotes.
	cust := e.client()
	cust.must("POST", "/api/auth/register", map[string]string{"email": "c@x.io", "password": "password1"})
	_, me := cust.req("GET", "/api/me", nil)
	custID := num(me["id"])
	prods := cust.must("GET", "/api/shop/products", nil)
	if items := prods["items"].([]any); len(items) != 1 {
		t.Fatalf("products: %v", prods)
	}
	sel := map[string]any{"model": "builder", "days": 90, "gb": 50, "ports": []map[string]any{{"port": "443"}, {"port": "25000", "class_id": premium}}}
	out := cust.must("POST", "/api/shop/quote", map[string]any{"product_id": prod, "selection": sel})
	// ports: 150000 (443 premium) + 80000 (25000 normal; client's class_id ignored) = 230000 × 2.7 = 621000; gb 100000
	if total := num(out["quote"].(map[string]any)["total"]); total != 621000+100000 {
		t.Fatalf("quote total %d: %v", total, out)
	}
	if code, out := cust.req("POST", "/api/shop/quote", map[string]any{"product_id": prod, "selection": map[string]any{"model": "builder", "days": 30, "gb": 10, "ports": []map[string]any{{"port": "80"}}}}); code != 400 {
		t.Fatalf("unsellable port: %d %v", code, out)
	}

	// Group discount applies after an admin puts the customer in VIP; simulator agrees.
	owner.must("PUT", "/api/admin/users/"+jsonID(custID), map[string]any{"group_id": vip})
	sel30 := map[string]any{"model": "builder", "days": 30, "gb": 10, "ports": []map[string]any{{"port": "21000"}}}
	q1 := cust.must("POST", "/api/shop/quote", map[string]any{"product_id": prod, "selection": sel30})
	q2 := owner.must("POST", "/api/admin/pricing/simulate", map[string]any{"product_id": prod, "selection": sel30, "user_id": custID})
	t1, t2 := num(q1["quote"].(map[string]any)["total"]), num(q2["quote"].(map[string]any)["total"])
	if t1 != 90000 || t1 != t2 { // (80000 + 20000) − 10%
		t.Fatalf("group quote %d simulate %d", t1, t2)
	}

	// Coupon.
	owner.must("POST", "/api/admin/discounts", map[string]any{"code": "off20", "kind": "percent", "value": 20, "enabled": true})
	sel30["coupon"] = "OFF20"
	q3 := cust.must("POST", "/api/shop/quote", map[string]any{"product_id": prod, "selection": sel30})
	if num(q3["quote"].(map[string]any)["total"]) != 72000 {
		t.Fatalf("coupon quote %v", q3)
	}
	sel30["coupon"] = "NOPE"
	if code, _ := cust.req("POST", "/api/shop/quote", map[string]any{"product_id": prod, "selection": sel30}); code != 400 {
		t.Fatal("unknown coupon accepted")
	}

	// Wallet top-up by card with receipt, admin approval.
	if code, _ := cust.req("POST", "/api/wallet/topup", map[string]any{"amount": 200000}); code != 400 {
		t.Fatalf("top-up without cards: %d", code)
	}
	owner.must("POST", "/api/admin/cards", map[string]any{"number": "6037991234567890", "holder": "Neo", "enabled": true})
	pay := cust.must("POST", "/api/wallet/topup", map[string]any{"amount": 200000})
	payID := jsonID(num(pay["id"]))
	if code, _ := cust.upload("/api/wallet/payments/"+payID+"/receipt", []byte("not an image at all")); code != 400 {
		t.Fatalf("non-image receipt accepted: %d", code)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...)
	other := e.client()
	other.req("POST", "/api/auth/register", map[string]string{"email": "z@x.io", "password": "password1"})
	if code, _ := other.upload("/api/wallet/payments/"+payID+"/receipt", png); code != 404 {
		t.Fatalf("receipt on someone else's payment: %d", code)
	}
	cust.mustUpload("/api/wallet/payments/"+payID+"/receipt", png)
	if code, _ := other.req("GET", "/api/wallet/payments/"+payID+"/receipt", nil); code != 404 {
		t.Fatalf("other user read receipt: %d", code)
	}
	resp, _ := owner.c.Get(e.srv.URL + "/api/admin/payments/" + payID + "/receipt")
	if resp.StatusCode != 200 {
		t.Fatalf("admin receipt: %d", resp.StatusCode)
	}
	resp.Body.Close()
	list := owner.must("GET", "/api/admin/payments?status=submitted", nil)
	if len(list["items"].([]any)) != 1 {
		t.Fatalf("pending list %v", list)
	}
	owner.must("POST", "/api/admin/payments/"+payID+"/approve", map[string]any{})
	if code, _ := owner.req("POST", "/api/admin/payments/"+payID+"/approve", map[string]any{}); code != 400 {
		t.Fatal("double approve")
	}
	wal := cust.must("GET", "/api/wallet", nil)
	if num(wal["balance"]) != num(pay["pay_amount"]) {
		t.Fatalf("balance %v vs pay %v", wal["balance"], pay["pay_amount"])
	}

	// Manual adjustment needs a reason and cannot overdraw unless allowed.
	if code, _ := owner.req("POST", "/api/admin/users/"+jsonID(custID)+"/wallet", map[string]any{"amount": 1000}); code != 400 {
		t.Fatal("adjust without note")
	}
	if code, _ := owner.req("POST", "/api/admin/users/"+jsonID(custID)+"/wallet", map[string]any{"amount": -10_000_000, "note": "x"}); code != 400 {
		t.Fatal("overdraw without allow_negative")
	}
	owner.must("POST", "/api/admin/users/"+jsonID(custID)+"/wallet", map[string]any{"amount": 5000, "note": "gift"})

	// Customers cannot touch the catalog.
	if code, _ := cust.req("POST", "/api/admin/products", map[string]any{}); code != 403 {
		t.Fatalf("customer wrote catalog: %d", code)
	}
}

func TestOutboundHiddenWithoutModule(t *testing.T) {
	e := newEnv(t, []string{license.ModPorts}, nil)
	owner := e.client()
	owner.req("POST", "/api/setup", map[string]string{"email": "o@x.io", "password": "password1"})
	owner.req("POST", "/api/admin/license/activate", map[string]string{"key": "HM-S"})
	owner.must("POST", "/api/admin/products", map[string]any{"type": "outbound", "title": "DE", "enabled": true, "stock": -1,
		"options": map[string]any{"models": []string{"builder"}, "durations": []int{30}}})
	cust := e.client()
	cust.req("POST", "/api/auth/register", map[string]string{"email": "c@x.io", "password": "password1"})
	out := cust.must("GET", "/api/shop/products", nil)
	if len(out["items"].([]any)) != 0 {
		t.Fatalf("outbound visible without license module: %v", out)
	}
}

func jsonID(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestPublicCatalog(t *testing.T) {
	e := newEnv(t, []string{license.ModPorts}, nil)
	anon := e.client()
	if code, out := anon.req("GET", "/api/public/catalog", nil); code != 200 || len(out["products"].([]any)) != 0 {
		t.Fatalf("before license: %d %v", code, out)
	}
	owner := e.client()
	owner.req("POST", "/api/setup", map[string]string{"email": "o@x.io", "password": "password1"})
	owner.req("POST", "/api/admin/license/activate", map[string]string{"key": "HM-P"})
	owner.must("POST", "/api/admin/groups", map[string]any{"name": "VIP"})
	owner.must("POST", "/api/admin/products", map[string]any{"type": "port", "title": "Open", "enabled": true, "stock": -1,
		"options": map[string]any{"models": []string{"package"}, "packages": []map[string]any{{"id": "a", "name": "A", "ports": 1, "gb": 50, "days": 30, "price": 300}, {"id": "b", "name": "B", "ports": 1, "gb": 10, "days": 30, "price": 100}}}})
	owner.must("POST", "/api/admin/products", map[string]any{"type": "port", "title": "VIP only", "enabled": true, "stock": -1, "visible_groups": []int{1},
		"options": map[string]any{"models": []string{"builder"}}})
	owner.must("POST", "/api/admin/products", map[string]any{"type": "outbound", "title": "No module", "enabled": true, "stock": -1,
		"options": map[string]any{"models": []string{"builder"}}})
	_, out := anon.req("GET", "/api/public/catalog", nil)
	ps := out["products"].([]any)
	if len(ps) != 1 || ps[0].(map[string]any)["title"] != "Open" || num(ps[0].(map[string]any)["from_price"]) != 100 {
		t.Fatalf("public catalog: %v", out)
	}
}
