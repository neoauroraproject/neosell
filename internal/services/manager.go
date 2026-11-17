// Package services turns paid orders into running services and keeps them
// in shape: port services on tunnel servers (through Neo Agent) and outbound
// services on 3x-ui panels; usage, limits, expiry, renewal and deletion.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"neosell/internal/catalog"
	"neosell/internal/db"
	"neosell/internal/jobs"
	"neosell/internal/license"
	"neosell/internal/pricing"
	"neosell/internal/secret"
	"neosell/internal/settings"
	"neosell/internal/wallet"
)

var (
	ErrNotFound  = errors.New("پیدا نشد")
	ErrForbidden = errors.New("دسترسی ندارید")
)

// Hub is the live connection to agents (implemented by agentgw).
type Hub interface {
	Push(nodeID int64)
	Online(nodeID int64) bool
	CheckPorts(ctx context.Context, nodeID int64, ports []int, proto string) ([]int, error)
}

// Notifier delivers user-facing notifications (panel + Telegram).
type Notifier interface {
	Notify(ctx context.Context, userID int64, kind, title, body string)
}

type Manager struct {
	DB        *db.DB
	Box       *secret.Box
	Wallet    *wallet.Wallet
	Catalog   *catalog.Store
	Settings  *settings.Store
	Jobs      *jobs.Queue
	License   *license.Manager
	Hub       Hub
	Notifier  Notifier
	Proxy     string // outbound proxy for 3x-ui panels (optional)
	PublicURL string // panel URL used in sub links and install scripts
	Now       func() time.Time
}

const (
	JobOutboundSync = "outbound.sync"
)

func (m *Manager) now() int64 {
	if m.Now != nil {
		return m.Now().Unix()
	}
	return time.Now().Unix()
}

// RegisterJobs wires background job handlers.
func (m *Manager) RegisterJobs() {
	m.Jobs.Register(JobOutboundSync, func(ctx context.Context, p json.RawMessage) error {
		var in struct {
			ServiceID int64 `json:"service_id"`
			Reset     bool  `json:"reset"`
		}
		if err := json.Unmarshal(p, &in); err != nil {
			return jobs.Permanent(err)
		}
		return m.syncOutbound(ctx, in.ServiceID, in.Reset)
	})
}

func (m *Manager) push(nodeID int64) {
	if m.Hub != nil && nodeID > 0 {
		m.Hub.Push(nodeID)
	}
}

func (m *Manager) pushAll(ctx context.Context) {
	ns, _ := m.Nodes(ctx, "tunnel")
	for _, n := range ns {
		m.push(n.ID)
	}
}

func (m *Manager) notify(ctx context.Context, userID int64, kind, title, body string) {
	if m.Notifier != nil {
		m.Notifier.Notify(ctx, userID, kind, title, body)
	}
}

func (m *Manager) gbBase(ctx context.Context) int64 {
	v, _ := strconv.ParseInt(m.Settings.String(ctx, "usage.gb_base"), 10, 64)
	if v <= 0 {
		return 1 << 30
	}
	return v
}

func (m *Manager) enqueueSync(ctx context.Context, serviceID int64, reset bool) {
	m.Jobs.Enqueue(ctx, JobOutboundSync, map[string]any{"service_id": serviceID, "reset": reset},
		jobs.Options{DedupeKey: "osync:" + strconv.FormatInt(serviceID, 10) + map[bool]string{true: ":r", false: ""}[reset], MaxAttempts: 20})
}

// Service statuses.
const (
	StProvisioning = "provisioning"
	StWaitingPeer  = "waiting_peer"
	StActive       = "active"
	StLimited      = "limited"
	StExpired      = "expired"
	StPaused       = "paused"
	StSuspended    = "suspended"
	StFailed       = "failed"
	StDeleted      = "deleted"
)

// blockedStatus: the service exists but must carry no traffic.
func blockedStatus(s string) bool {
	switch s {
	case StLimited, StExpired, StPaused, StSuspended:
		return true
	}
	return false
}

func liveStatus(s string) bool { return s != StDeleted && s != StFailed }

var _ = pricing.ModelPackage
