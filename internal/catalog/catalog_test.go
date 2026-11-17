package catalog

import "testing"

func TestClassify(t *testing.T) {
	classes := []PortClass{
		{ID: 1, Kind: "range", Expr: "2000-2999"},
		{ID: 2, Kind: "specific", Expr: "443,2053"},
		{ID: 3, Kind: "random"},
	}
	cases := map[string]int64{"443": 2, "2053": 2 /* specific beats range */, "2500": 1, "": 3}
	for port, want := range cases {
		if got, err := Classify(classes, port); err != nil || got != want {
			t.Errorf("%q: got %d err %v want %d", port, got, err, want)
		}
	}
	for _, bad := range []string{"80", "70000", "abc"} {
		if _, err := Classify(classes, bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	if _, err := Classify(classes[:2], ""); err == nil {
		t.Error("random without a random class should be refused")
	}
}

func TestExpandPorts(t *testing.T) {
	ps, err := ExpandPorts("443, 2053-2055", 10)
	if err != nil || len(ps) != 4 || ps[3] != 2055 {
		t.Fatalf("%v %v", ps, err)
	}
	for _, bad := range []string{"443,443", "10-1", "1-20", "0"} {
		if _, err := ExpandPorts(bad, 10); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
