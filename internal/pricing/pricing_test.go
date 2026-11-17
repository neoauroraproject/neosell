package pricing

import (
	"errors"
	"testing"
)

func portProduct() Product {
	return Product{ID: 1, Type: "port", Title: "Tehran", Options: Options{
		Models:      []Model{ModelBuilder, ModelPackage, ModelPerPort, ModelUnlimited, ModelPerGB, ModelPayg},
		Durations:   []int{30, 90},
		MaxPorts:    5,
		MinGB:       10,
		MaxGB:       1000,
		GBStep:      10,
		IncludedIPs: 2,
		MaxIPs:      10,
		PerPortGB:   50,
		Packages:    []Package{{ID: "p1", Name: "Starter", Ports: 1, GB: 100, Days: 30, Price: 150000}},
		Cores:       []string{"neotunnel", "kariz"},
	}}
}

func rules() []Rule {
	return []Rule{
		{ID: 1, Component: CompPort, Value: 80000, Enabled: true},                                                 // any port
		{ID: 2, Component: CompPort, Scope: map[string]string{"class_id": "7"}, Value: 150000, Enabled: true},     // premium class
		{ID: 3, Component: CompGB, Tiers: []Tier{{Upto: 100, Price: 2000}, {Price: 1500}}, Enabled: true},         // graduated
		{ID: 4, Component: CompDuration, Tiers: []Tier{{Days: 90, Mult: 2.7}}, Enabled: true},                     // 3 months = 2.7×
		{ID: 5, Component: CompIP, Value: 10000, Enabled: true},                                                   // per extra IP
		{ID: 6, Component: CompRate, Value: 1000, Enabled: true},                                                  // per Mbps
		{ID: 7, Component: CompPort, Scope: map[string]string{"core": "kariz"}, Value: 60000, Enabled: true},      // cheaper on kariz
		{ID: 8, Component: CompRenew, Value: 0.9, Enabled: true},                                                  // renew 10% off
		{ID: 9, Component: CompAddon, Scope: map[string]string{"addon_kind": "days"}, Value: 3000, Enabled: true}, // per extra day
		{ID: 10, Component: CompPaygGB, Value: 2500, Enabled: true},
	}
}

func mustPrice(t *testing.T, p Product, r []Rule, s Selection, c Context) *Quote {
	t.Helper()
	q, err := Price(p, r, s, c)
	if err != nil {
		t.Fatalf("price: %v", err)
	}
	return q
}

func TestBuilderGraduatedGBAndClasses(t *testing.T) {
	q := mustPrice(t, portProduct(), rules(), Selection{Model: ModelBuilder, Days: 30, GB: 150,
		Ports: []PortSel{{ClassID: 1}, {ClassID: 7}}}, Context{})
	// ports: 80000 + 150000; gb: 100×2000 + 50×1500 = 275000
	if q.Total != 80000+150000+275000 {
		t.Fatalf("total %d lines %+v", q.Total, q.Lines)
	}
	if q.Spec.Ports != 2 || q.Spec.GB != 150 || q.Spec.Days != 30 || q.Spec.MaxIPs != 2 {
		t.Fatalf("spec %+v", q.Spec)
	}
}

func TestDurationFactorOnlyOnTimedLines(t *testing.T) {
	q := mustPrice(t, portProduct(), rules(), Selection{Model: ModelBuilder, Days: 90, GB: 10,
		Ports: []PortSel{{ClassID: 1}}}, Context{})
	// port 80000 × 2.7 = 216000 ; gb 20000 not multiplied
	if q.Total != 216000+20000 {
		t.Fatalf("total %d lines %+v", q.Total, q.Lines)
	}
}

func TestMoreSpecificRuleWins(t *testing.T) {
	q := mustPrice(t, portProduct(), rules(), Selection{Model: ModelPerPort, Days: 30, Core: "kariz",
		Ports: []PortSel{{ClassID: 1}}}, Context{})
	if q.Total != 60000 || q.Spec.GB != 50 {
		t.Fatalf("kariz port: %d spec %+v", q.Total, q.Spec)
	}
	// class_id=7 and core=kariz both have one scope key: tie broken by priority, then newest id.
	r := rules()
	r = append(r, Rule{ID: 11, Component: CompPort, Scope: map[string]string{"class_id": "7", "core": "kariz"}, Value: 120000, Enabled: true})
	q = mustPrice(t, portProduct(), r, Selection{Model: ModelPerPort, Days: 30, Core: "kariz", Ports: []PortSel{{ClassID: 7}}}, Context{})
	if q.Total != 120000 {
		t.Fatalf("two-key rule should win: %d", q.Total)
	}
}

func TestGroupAndUserOverrides(t *testing.T) {
	r := append(rules(),
		Rule{ID: 20, Component: CompPort, GroupID: 3, Value: 70000, Enabled: true},
		Rule{ID: 21, Component: CompPort, UserID: 42, Value: 50000, Enabled: true},
	)
	sel := Selection{Model: ModelPerPort, Days: 30, Ports: []PortSel{{ClassID: 1}}}
	if q := mustPrice(t, portProduct(), r, sel, Context{GroupID: 3}); q.Total != 70000 {
		t.Fatalf("group price: %d", q.Total)
	}
	if q := mustPrice(t, portProduct(), r, sel, Context{GroupID: 3, UserID: 42}); q.Total != 50000 {
		t.Fatalf("user override: %d", q.Total)
	}
	// Group discount on top, then rounding.
	q := mustPrice(t, portProduct(), r, sel, Context{GroupID: 3, GroupDiscountPct: 15, RoundTo: 1000})
	if q.Total != 60000 { // 70000 − 10500 = 59500 → 60000
		t.Fatalf("group discount + round: %d %+v", q.Total, q.Adjustments)
	}
}

func TestSaleWindowAndCoupon(t *testing.T) {
	r := append(rules(), Rule{ID: 30, Component: CompSale, Value: 20, ValidFrom: 100, ValidTo: 200, Enabled: true})
	sel := Selection{Model: ModelPerPort, Days: 30, Ports: []PortSel{{ClassID: 1}}}
	if q := mustPrice(t, portProduct(), r, sel, Context{Now: 50}); q.Total != 80000 {
		t.Fatalf("before sale: %d", q.Total)
	}
	if q := mustPrice(t, portProduct(), r, sel, Context{Now: 150}); q.Total != 64000 {
		t.Fatalf("during sale: %d", q.Total)
	}
	q := mustPrice(t, portProduct(), r, sel, Context{Now: 150, Coupon: &Coupon{Code: "OFF", Kind: "amount", Value: 100000}})
	if q.Total != 0 {
		t.Fatalf("coupon larger than total must floor at 0: %d", q.Total)
	}
	if _, err := Price(portProduct(), r, sel, Context{Coupon: &Coupon{Code: "BIG", Kind: "percent", Value: 10, MinAmount: 500000}}); err == nil {
		t.Fatal("coupon min amount not enforced")
	}
}

func TestPackageAndOverride(t *testing.T) {
	sel := Selection{Model: ModelPackage, PackageID: "p1", Ports: []PortSel{{ClassID: 1}}}
	q := mustPrice(t, portProduct(), rules(), sel, Context{})
	if q.Total != 150000 || q.Spec.GB != 100 || q.Spec.Days != 30 {
		t.Fatalf("package: %d %+v", q.Total, q.Spec)
	}
	r := append(rules(), Rule{ID: 40, Component: CompPackage, Scope: map[string]string{"package_id": "p1"}, Value: 99000, GroupID: 5, Enabled: true})
	if q := mustPrice(t, portProduct(), r, sel, Context{GroupID: 5}); q.Total != 99000 {
		t.Fatalf("package override for group: %d", q.Total)
	}
	if _, err := Price(portProduct(), rules(), Selection{Model: ModelPackage, PackageID: "nope"}, Context{}); err == nil {
		t.Fatal("unknown package accepted")
	}
}

func TestRenewAndAddon(t *testing.T) {
	sel := Selection{Kind: "renew", Model: ModelPerPort, Days: 30, Ports: []PortSel{{ClassID: 1}}}
	if q := mustPrice(t, portProduct(), rules(), sel, Context{}); q.Total != 72000 {
		t.Fatalf("renew: %d", q.Total)
	}
	add := Selection{Kind: "addon", Model: ModelBuilder, AddonKind: "days", AddonQty: 10}
	if q := mustPrice(t, portProduct(), rules(), add, Context{}); q.Total != 30000 {
		t.Fatalf("addon days: %d", q.Total)
	}
	gb := Selection{Kind: "addon", Model: ModelBuilder, AddonKind: "gb", AddonQty: 120}
	if q := mustPrice(t, portProduct(), rules(), gb, Context{}); q.Total != 230000 { // falls back to GB tiers
		t.Fatalf("addon gb: %d", q.Total)
	}
	if _, err := Price(portProduct(), rules(), Selection{Kind: "addon", AddonKind: "ip", AddonQty: 1}, Context{}); err == nil {
		t.Fatal("addon without price accepted")
	}
}

func TestUnlimitedIPsAndRate(t *testing.T) {
	q := mustPrice(t, portProduct(), rules(), Selection{Model: ModelUnlimited, Days: 30, RateMbit: 50, MaxIPs: 4,
		Ports: []PortSel{{ClassID: 1}}}, Context{})
	// port 80000 + rate 50×1000 + 2 extra IPs × 10000
	if q.Total != 80000+50000+20000 || q.Spec.GB != 0 {
		t.Fatalf("unlimited: %d %+v", q.Total, q.Lines)
	}
	if _, err := Price(portProduct(), rules(), Selection{Model: ModelUnlimited, Days: 30, Ports: []PortSel{{ClassID: 1}}}, Context{}); err == nil {
		t.Fatal("unlimited without rate cap accepted")
	}
}

func TestValidation(t *testing.T) {
	p := portProduct()
	cases := map[string]Selection{
		"bad duration":   {Model: ModelBuilder, Days: 45, GB: 10, Ports: []PortSel{{ClassID: 1}}},
		"too many ports": {Model: ModelPerPort, Days: 30, Ports: make([]PortSel, 6)},
		"no ports":       {Model: ModelPerPort, Days: 30},
		"gb step":        {Model: ModelBuilder, Days: 30, GB: 15, Ports: []PortSel{{ClassID: 1}}},
		"gb max":         {Model: ModelBuilder, Days: 30, GB: 2000, Ports: []PortSel{{ClassID: 1}}},
		"core":           {Model: ModelPerPort, Days: 30, Core: "backpack", Ports: []PortSel{{ClassID: 1}}},
		"ips":            {Model: ModelPerPort, Days: 30, MaxIPs: 11, Ports: []PortSel{{ClassID: 1}}},
		"model":          {Model: "weird", Days: 30},
	}
	for name, sel := range cases {
		_, err := Price(p, rules(), sel, Context{})
		var pe Error
		if !errors.As(err, &pe) {
			t.Errorf("%s: expected pricing error, got %v", name, err)
		}
	}
	// Missing price for a required component is an error, not a free item.
	if _, err := Price(p, []Rule{{ID: 1, Component: CompGB, Value: 1, Enabled: true}}, Selection{Model: ModelPerPort, Days: 30, Ports: []PortSel{{ClassID: 1}}}, Context{}); err == nil {
		t.Fatal("unpriced port was free")
	}
	// Disabled rules are ignored.
	r := rules()
	r[0].Enabled = false
	if _, err := Price(p, r, Selection{Model: ModelPerPort, Days: 30, Ports: []PortSel{{ClassID: 1}}}, Context{}); err == nil {
		t.Fatal("disabled rule used")
	}
}

func TestOutboundMultiLocation(t *testing.T) {
	p := Product{ID: 2, Type: "outbound", Options: Options{Models: []Model{ModelBuilder}, Durations: []int{30}, Locations: []int64{1, 2}}}
	r := []Rule{
		{ID: 1, Component: CompGB, Value: 1000, Enabled: true},
		{ID: 2, Component: CompGB, Scope: map[string]string{"location_id": "2"}, Value: 3000, Enabled: true},
		{ID: 3, Component: CompLocation, Scope: map[string]string{"location_id": "2"}, Value: 20000, Enabled: true},
	}
	q := mustPrice(t, p, r, Selection{Model: ModelBuilder, Days: 30, Locations: []LocSel{{1, 100}, {2, 50}}}, Context{})
	// DE: 100×1000 ; NL: 20000 + 50×3000
	if q.Total != 100000+20000+150000 || len(q.Spec.Locations) != 2 || q.Spec.Locations[1].GB != 50 {
		t.Fatalf("outbound: %d %+v", q.Total, q.Spec)
	}
	if _, err := Price(p, r, Selection{Model: ModelBuilder, Days: 30, Locations: []LocSel{{3, 10}}}, Context{}); err == nil {
		t.Fatal("location not allowed accepted")
	}
	if _, err := Price(p, r, Selection{Model: ModelBuilder, Days: 30, Locations: []LocSel{{1, 10}, {1, 10}}}, Context{}); err == nil {
		t.Fatal("duplicate location accepted")
	}
}

func TestPayg(t *testing.T) {
	q := mustPrice(t, portProduct(), rules(), Selection{Model: ModelPayg, Ports: []PortSel{{ClassID: 1}}}, Context{})
	if q.Total != 0 || q.PaygPerGB != 2500 {
		t.Fatalf("payg: %+v", q)
	}
}

func TestGraduatedEdges(t *testing.T) {
	r := &Rule{Tiers: []Tier{{Upto: 10, Price: 5}, {Upto: 20, Price: 3}}}
	if got := graduated(r, 25); got != 10*5+10*3+5*3 {
		t.Fatalf("beyond last bounded tier: %d", got)
	}
	if got := graduated(&Rule{Value: 2.5}, 3); got != 8 { // 7.5 rounds to 8
		t.Fatalf("flat: %d", got)
	}
}

func TestValidateRule(t *testing.T) {
	if ValidateRule(Rule{Component: "nope"}) == nil ||
		ValidateRule(Rule{Component: CompSale, Value: 150}) == nil ||
		ValidateRule(Rule{Component: CompDuration}) == nil ||
		ValidateRule(Rule{Component: CompGB, Value: -1}) == nil {
		t.Fatal("bad rules accepted")
	}
	if err := ValidateRule(Rule{Component: CompDuration, Tiers: []Tier{{Days: 90, Mult: 2.7}}}); err != nil {
		t.Fatal(err)
	}
}
