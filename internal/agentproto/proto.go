// Package agentproto defines the messages between the panel and Neo Agent.
//
// The agent connects to the panel over a WebSocket (so tunnel servers need no
// open inbound port) and the panel pushes the complete desired state. The
// agent converges to it idempotently: tunnels, forwards and the nftables
// table that counts and limits every sold port. Usage is reported as absolute
// totals, so a lost or repeated report can never double-count.
package agentproto

import "encoding/json"

const Version = 1

// Envelope wraps every message.
type Envelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"` // request/response correlation
	Data json.RawMessage `json:"data,omitempty"`
}

// Message types.
const (
	TypeHello       = "hello"        // agent → panel, first message
	TypeState       = "state"        // panel → agent, desired state
	TypeApplied     = "applied"      // agent → panel, result of applying a state
	TypeUsage       = "usage"        // agent → panel, absolute counters
	TypeEvents      = "events"       // agent → panel, quota/expiry hits
	TypeTunnels     = "tunnels"      // agent → panel, tunnel status (peer setup, connected)
	TypeCheckPorts  = "check_ports"  // panel → agent (request)
	TypePortsResult = "ports_result" // agent → panel (response)
	TypeError       = "error"
)

type Hello struct {
	Proto    int      `json:"proto"`
	Version  string   `json:"version"`
	Arch     string   `json:"arch"`
	Hostname string   `json:"hostname"`
	IPs      []string `json:"ips"`
	NFT      bool     `json:"nft"` // nft binary available
}

type Port struct {
	Port   int    `json:"port"`
	ToPort int    `json:"to_port"`
	Proto  string `json:"proto"` // tcp | udp | both
}

// Service is one sold port service as the agent must enforce it.
type Service struct {
	ID         int64  `json:"id"`
	Tunnel     string `json:"tunnel"` // tunnel name the ports go through
	Ports      []Port `json:"ports"`
	QuotaBytes int64  `json:"quota_bytes"` // 0 = unlimited
	UsedBytes  int64  `json:"used_bytes"`  // consumed this period as the panel knows it
	Period     int64  `json:"period"`      // period start; a change resets the quota
	ExpiresAt  int64  `json:"expires_at"`  // 0 = never
	RateMbit   int    `json:"rate_mbit"`
	MaxIPs     int    `json:"max_ips"`
	Blocked    bool   `json:"blocked"` // paused/suspended/limited/expired by the panel
}

// Tunnel is a transport the agent must keep running.
type Tunnel struct {
	Name   string `json:"name"`
	Core   string `json:"core"`
	Driver string `json:"driver"` // neotunnel | template
	Mode   string `json:"mode"`   // shared | dedicated
	// template driver
	BinaryURL    string `json:"binary_url,omitempty"`
	BinarySHA256 string `json:"binary_sha256,omitempty"`
	Config       string `json:"config,omitempty"` // rendered server config
	ConfigExt    string `json:"config_ext,omitempty"`
	Exec         string `json:"exec,omitempty"` // {bin} and {config} are replaced
	// neotunnel driver
	NTTunnel      string `json:"nt_tunnel,omitempty"`       // existing tunnel (shared)
	NTPeerName    string `json:"nt_peer_name,omitempty"`    // dedicated: peer to create
	NTPeerAddress string `json:"nt_peer_address,omitempty"` // dedicated: customer's IP
	NTMode        string `json:"nt_mode,omitempty"`         // NeoTunnel tunnel mode
}

type NeoTunnel struct {
	BaseURL string `json:"base_url"` // local panel URL incl. secret path
	Token   string `json:"token"`
}

type State struct {
	Rev       int64      `json:"rev"`
	Services  []Service  `json:"services"`
	Tunnels   []Tunnel   `json:"tunnels"`
	NeoTunnel *NeoTunnel `json:"neotunnel,omitempty"`
}

type Applied struct {
	Rev    int64    `json:"rev"`
	Errors []string `json:"errors,omitempty"`
}

type Counter struct {
	ServiceID int64 `json:"service_id"`
	Up        int64 `json:"up"`   // absolute since the service appeared on this agent
	Down      int64 `json:"down"` // absolute
}

type Usage struct {
	Counters []Counter `json:"counters"`
}

type Event struct {
	ServiceID int64  `json:"service_id"`
	Kind      string `json:"kind"` // quota | expired
	At        int64  `json:"at"`
}

type TunnelStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // running | waiting_peer | connected | error
	PeerSetup string `json:"peer_setup,omitempty"`
	Error     string `json:"error,omitempty"`
}

type CheckPorts struct {
	Ports []int  `json:"ports"`
	Proto string `json:"proto"`
}

type PortsResult struct {
	Busy []int `json:"busy"`
}

func Wrap(typ, id string, v any) ([]byte, error) {
	var raw json.RawMessage
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(Envelope{Type: typ, ID: id, Data: raw})
}
