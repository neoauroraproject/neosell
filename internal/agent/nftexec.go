package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// NFT is the agent's view of nftables (a fake in tests).
type NFT interface {
	Available() bool
	Apply(script string) error
	// ResetCounters returns every counter's bytes and zeroes them atomically.
	ResetCounters() (map[string]int64, error)
	// Quotas returns each quota object's used bytes.
	Quotas() (map[string]int64, error)
}

type nftCLI struct{ bin string }

func NewNFT() NFT {
	bin, err := exec.LookPath("nft")
	if err != nil {
		return &nftCLI{}
	}
	return &nftCLI{bin: bin}
}

func (n *nftCLI) Available() bool { return n.bin != "" }

func (n *nftCLI) Apply(script string) error {
	if n.bin == "" {
		return fmt.Errorf("nft not installed (apt install nftables)")
	}
	cmd := exec.Command(n.bin, "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft: %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (n *nftCLI) json(args ...string) ([]byte, error) {
	if n.bin == "" {
		return nil, fmt.Errorf("nft not installed")
	}
	out, err := exec.Command(n.bin, append([]string{"-j"}, args...)...).Output()
	if err != nil {
		// The table does not exist yet (nothing sold): no counters.
		if ee, ok := err.(*exec.ExitError); ok && bytes.Contains(ee.Stderr, []byte("No such file")) {
			return []byte(`{"nftables":[]}`), nil
		}
		return nil, err
	}
	return out, nil
}

func (n *nftCLI) ResetCounters() (map[string]int64, error) {
	raw, err := n.json("reset", "counters", "table", "inet", TableName)
	if err != nil {
		return nil, err
	}
	return parseNFTObjects(raw, "counter", "bytes")
}

func (n *nftCLI) Quotas() (map[string]int64, error) {
	raw, err := n.json("list", "quotas", "table", "inet", TableName)
	if err != nil {
		return nil, err
	}
	return parseNFTObjects(raw, "quota", "used")
}

// parseNFTObjects reads `nft -j` output: {"nftables":[{"counter":{"name":…,"bytes":…}}]}.
func parseNFTObjects(raw []byte, kind, field string) (map[string]int64, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, item := range doc.Nftables {
		obj, ok := item[kind]
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal(obj, &m) != nil {
			continue
		}
		if tbl, _ := m["table"].(string); tbl != "" && tbl != TableName {
			continue
		}
		name, _ := m["name"].(string)
		v, _ := m[field].(float64)
		if name != "" {
			out[name] = int64(v)
		}
	}
	return out, nil
}
