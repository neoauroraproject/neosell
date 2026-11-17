package agent

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"neosell/internal/agentproto"
)

// TableName is the agent's own nftables table; nothing else is touched.
const TableName = "neosell"

// svcRuntime is what the ruleset needs per service beyond the panel's spec.
type svcRuntime struct {
	agentproto.Service
	QuotaUsed int64 // bytes to pre-load into the quota object
	Block     bool  // blocked by panel, expired or over quota
}

func objName(id int64, suffix string) string { return "s" + strconv.FormatInt(id, 10) + "_" + suffix }

func protoSet(p string) string {
	switch p {
	case "udp":
		return "udp"
	case "both":
		return "{ tcp, udp }"
	default:
		return "tcp"
	}
}

// BuildRuleset renders the complete table. It is applied atomically with
// `nft -f -` (delete + recreate in one transaction), so a sold port is never
// unprotected during an update.
//
// Counting uses conntrack's ORIGINAL tuple: a packet belongs to a sold port
// when its connection was opened to that port on one of this server's own
// addresses. That works the same for kernel DNAT forwards (NeoTunnel) and for
// user-space cores (Kariz, Backpack), counts each packet once, and ignores the
// server's own outgoing connections to remote ports with the same number.
func BuildRuleset(services []svcRuntime, localIPs []string) string {
	var v4, v6 []string
	for _, ip := range localIPs {
		p := net.ParseIP(ip)
		if p == nil || p.IsLoopback() {
			continue
		}
		if p.To4() != nil {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	ss := append([]svcRuntime(nil), services...)
	sort.Slice(ss, func(i, j int) bool { return ss[i].ID < ss[j].ID })

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...); b.WriteByte('\n') }

	w("table inet %s", TableName)
	w("delete table inet %s", TableName)
	w("table inet %s {", TableName)
	if len(v4) > 0 {
		w("\tset local4 { type ipv4_addr; elements = { %s } }", strings.Join(v4, ", "))
	} else {
		w("\tset local4 { type ipv4_addr; }")
	}
	if len(v6) > 0 {
		w("\tset local6 { type ipv6_addr; elements = { %s } }", strings.Join(v6, ", "))
	} else {
		w("\tset local6 { type ipv6_addr; }")
	}

	for _, s := range ss {
		if len(s.Ports) == 0 {
			continue
		}
		w("\tcounter %s { }", objName(s.ID, "up"))
		w("\tcounter %s { }", objName(s.ID, "down"))
		if s.QuotaBytes > 0 {
			used := s.QuotaUsed
			if used < 0 {
				used = 0
			}
			w("\tquota %s { over %d bytes used %d bytes }", objName(s.ID, "q"), s.QuotaBytes, used)
		}
		if s.RateMbit > 0 {
			kb := s.RateMbit * 125 // Mbit/s → kbytes/s
			w("\tlimit %s { rate over %d kbytes/second burst %d kbytes }", objName(s.ID, "lu"), kb, kb)
			w("\tlimit %s { rate over %d kbytes/second burst %d kbytes }", objName(s.ID, "ld"), kb, kb)
		}
		if s.MaxIPs > 0 {
			w("\tset %s { type ipv4_addr; size %d; flags dynamic, timeout; timeout 5m; }", objName(s.ID, "ip4"), s.MaxIPs)
			w("\tset %s { type ipv6_addr; size %d; flags dynamic, timeout; timeout 5m; }", objName(s.ID, "ip6"), s.MaxIPs)
		}
	}

	w("\tchain pre { type filter hook prerouting priority -150; policy accept; jump sold; }")
	w("\tchain out { type filter hook output priority -150; policy accept; jump sold; }")

	// Dispatch: one rule per (service, protocol, family).
	w("\tchain sold {")
	for _, s := range ss {
		if len(s.Ports) == 0 {
			continue
		}
		byProto := map[string][]int{}
		for _, p := range s.Ports {
			byProto[p.Proto] = append(byProto[p.Proto], p.Port)
		}
		protos := make([]string, 0, len(byProto))
		for p := range byProto {
			protos = append(protos, p)
		}
		sort.Strings(protos)
		for _, proto := range protos {
			ports := byProto[proto]
			sort.Ints(ports)
			list := make([]string, len(ports))
			for i, p := range ports {
				list[i] = strconv.Itoa(p)
			}
			set := "{ " + strings.Join(list, ", ") + " }"
			w("\t\tct original ip daddr @local4 meta l4proto %s ct original proto-dst %s jump %s", protoSet(proto), set, objName(s.ID, "c"))
			w("\t\tct original ip6 daddr @local6 meta l4proto %s ct original proto-dst %s jump %s", protoSet(proto), set, objName(s.ID, "c"))
		}
	}
	w("\t}")

	for _, s := range ss {
		if len(s.Ports) == 0 {
			continue
		}
		w("\tchain %s {", objName(s.ID, "c"))
		if s.Block {
			w("\t\tdrop")
		} else {
			if s.MaxIPs > 0 {
				// Keep up to MaxIPs client addresses (5 min after their last packet);
				// a new address beyond that is dropped.
				w("\t\tmeta nfproto ipv4 ct direction original update @%s { ip saddr }", objName(s.ID, "ip4"))
				w("\t\tmeta nfproto ipv4 ct direction original ip saddr != @%s drop", objName(s.ID, "ip4"))
				w("\t\tmeta nfproto ipv6 ct direction original update @%s { ip6 saddr }", objName(s.ID, "ip6"))
				w("\t\tmeta nfproto ipv6 ct direction original ip6 saddr != @%s drop", objName(s.ID, "ip6"))
			}
			if s.QuotaBytes > 0 {
				w("\t\tquota name \"%s\" drop", objName(s.ID, "q"))
			}
			if s.RateMbit > 0 {
				w("\t\tct direction original limit name \"%s\" drop", objName(s.ID, "lu"))
				w("\t\tct direction reply limit name \"%s\" drop", objName(s.ID, "ld"))
			}
			w("\t\tct direction original counter name \"%s\"", objName(s.ID, "up"))
			w("\t\tct direction reply counter name \"%s\"", objName(s.ID, "down"))
		}
		w("\t}")
	}
	w("}")
	return b.String()
}

// parseObjName returns (serviceID, suffix) for "s12_up".
func parseObjName(name string) (int64, string, bool) {
	if !strings.HasPrefix(name, "s") {
		return 0, "", false
	}
	idStr, suffix, ok := strings.Cut(name[1:], "_")
	if !ok {
		return 0, "", false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, "", false
	}
	return id, suffix, true
}
