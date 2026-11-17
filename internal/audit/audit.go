// Package audit records who changed what, with before/after snapshots.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"neosell/internal/db"
)

type Log struct{ db *db.DB }

func New(d *db.DB) *Log { return &Log{db: d} }

type Entry struct {
	ActorID    int64
	Action     string
	TargetType string
	TargetID   string
	Before     any
	After      any
	IP         string
}

func js(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}

// Write never fails the caller's request; errors are only logged.
func (l *Log) Write(ctx context.Context, e Entry) {
	var actor any
	if e.ActorID > 0 {
		actor = e.ActorID
	}
	_, err := l.db.ExecContext(ctx, `INSERT INTO audit_logs(actor_id, action, target_type, target_id, before_json, after_json, ip, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, actor, e.Action, e.TargetType, e.TargetID, js(e.Before), js(e.After), e.IP, db.Now())
	if err != nil {
		slog.Error("audit write failed", "action", e.Action, "err", err)
	}
}

type Row struct {
	ID         int64           `json:"id"`
	ActorID    int64           `json:"actor_id"`
	ActorEmail string          `json:"actor_email"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	IP         string          `json:"ip"`
	CreatedAt  int64           `json:"created_at"`
}

// List returns entries newest first; beforeID > 0 pages backwards.
func (l *Log) List(ctx context.Context, beforeID int64, limit int) ([]Row, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT a.id, COALESCE(a.actor_id,0), COALESCE(u.email,''), a.action, COALESCE(a.target_type,''), COALESCE(a.target_id,''),
		COALESCE(a.before_json,''), COALESCE(a.after_json,''), COALESCE(a.ip,''), a.created_at
		FROM audit_logs a LEFT JOIN users u ON u.id = a.actor_id`
	args := []any{}
	if beforeID > 0 {
		q += ` WHERE a.id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY a.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		var before, after string
		if err := rows.Scan(&r.ID, &r.ActorID, &r.ActorEmail, &r.Action, &r.TargetType, &r.TargetID, &before, &after, &r.IP, &r.CreatedAt); err != nil {
			return nil, err
		}
		if before != "" {
			r.Before = json.RawMessage(before)
		}
		if after != "" {
			r.After = json.RawMessage(after)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
