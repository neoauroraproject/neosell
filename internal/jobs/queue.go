// Package jobs is a small durable job queue on top of SQLite. It replaces
// Redis/BullMQ: jobs survive restarts, retry with exponential backoff and can
// be de-duplicated by key.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"neosell/internal/db"
)

// Handler processes one job. Returning an error schedules a retry; returning
// a Permanent error fails the job at once.
type Handler func(ctx context.Context, payload json.RawMessage) error

type permanent struct{ error }

func Permanent(err error) error { return permanent{err} }

type Queue struct {
	db       *db.DB
	mu       sync.RWMutex
	handlers map[string]Handler
	wake     chan struct{}
	lease    time.Duration
}

func New(d *db.DB) *Queue {
	return &Queue{db: d, handlers: map[string]Handler{}, wake: make(chan struct{}, 1), lease: 5 * time.Minute}
}

func (q *Queue) Register(kind string, h Handler) {
	q.mu.Lock()
	q.handlers[kind] = h
	q.mu.Unlock()
}

type Options struct {
	Delay       time.Duration
	DedupeKey   string // skip enqueue if a pending/running job has this key
	MaxAttempts int
}

// Enqueue stores a job. With DedupeKey, an existing unfinished job wins and
// (0, nil) is returned.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, o Options) (int64, error) {
	return q.enqueue(ctx, q.db, kind, payload, o)
}

// EnqueueTx enqueues inside the caller's transaction, so a job is created only
// if the business change that needs it commits.
func (q *Queue) EnqueueTx(ctx context.Context, tx *sql.Tx, kind string, payload any, o Options) (int64, error) {
	return q.enqueue(ctx, tx, kind, payload, o)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (q *Queue) enqueue(ctx context.Context, ex execer, kind string, payload any, o Options) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	max := o.MaxAttempts
	if max <= 0 {
		max = 8
	}
	now := time.Now()
	var dedupe any
	if o.DedupeKey != "" {
		dedupe = o.DedupeKey
		// Finished jobs release their key so the same work can be queued again.
		if _, err := ex.ExecContext(ctx, `UPDATE jobs SET dedupe_key = NULL WHERE dedupe_key = ? AND status IN ('done','failed')`, o.DedupeKey); err != nil {
			return 0, err
		}
	}
	res, err := ex.ExecContext(ctx, `INSERT INTO jobs(kind, payload, dedupe_key, run_at, max_attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(dedupe_key) DO NOTHING`,
		kind, string(raw), dedupe, now.Add(o.Delay).Unix(), max, now.Unix(), now.Unix())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if n, _ := res.RowsAffected(); n == 0 {
		id = 0
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return id, nil
}

// Run processes due jobs with `workers` goroutines until ctx is cancelled.
func (q *Queue) Run(ctx context.Context, workers int) {
	if workers < 1 {
		workers = 1
	}
	// Jobs left "running" by a crash become due again once their lease ends.
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.worker(ctx)
		}()
	}
	wg.Wait()
}

func (q *Queue) worker(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		for {
			ran, err := q.RunOne(ctx)
			if err != nil {
				slog.Error("jobs: claim failed", "err", err)
				break
			}
			if !ran || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-q.wake:
		}
	}
}

// RunOne claims and runs a single due job. It reports whether a job ran.
func (q *Queue) RunOne(ctx context.Context) (bool, error) {
	now := time.Now().Unix()
	var (
		id       int64
		kind     string
		payload  string
		attempts int
		max      int
	)
	// Atomic claim: a single UPDATE … RETURNING so two workers never take the same job.
	err := q.db.QueryRowContext(ctx, `UPDATE jobs SET status = 'running', attempts = attempts + 1,
			locked_until = ?, updated_at = ?
		WHERE id = (SELECT id FROM jobs
			WHERE (status = 'pending' AND run_at <= ?) OR (status = 'running' AND locked_until < ?)
			ORDER BY run_at, id LIMIT 1)
		RETURNING id, kind, payload, attempts, max_attempts`,
		now+int64(q.lease.Seconds()), now, now, now).Scan(&id, &kind, &payload, &attempts, &max)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	q.mu.RLock()
	h := q.handlers[kind]
	q.mu.RUnlock()

	var runErr error
	if h == nil {
		runErr = Permanent(fmt.Errorf("no handler for job kind %q", kind))
	} else {
		runErr = safeRun(ctx, h, json.RawMessage(payload))
	}
	q.finish(id, kind, attempts, max, runErr)
	return true, nil
}

func safeRun(ctx context.Context, h Handler, p json.RawMessage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	jctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	return h(jctx, p)
}

func (q *Queue) finish(id int64, kind string, attempts, max int, runErr error) {
	ctx := context.Background()
	now := time.Now()
	if runErr == nil {
		q.db.ExecContext(ctx, `UPDATE jobs SET status='done', last_error='', locked_until=0, updated_at=? WHERE id=?`, now.Unix(), id)
		return
	}
	var p permanent
	if errors.As(runErr, &p) || attempts >= max {
		slog.Error("jobs: failed", "id", id, "kind", kind, "attempts", attempts, "err", runErr)
		q.db.ExecContext(ctx, `UPDATE jobs SET status='failed', last_error=?, locked_until=0, updated_at=? WHERE id=?`, runErr.Error(), now.Unix(), id)
		return
	}
	next := now.Add(Backoff(attempts))
	slog.Warn("jobs: retry", "id", id, "kind", kind, "attempt", attempts, "next", next.Format(time.RFC3339), "err", runErr)
	q.db.ExecContext(ctx, `UPDATE jobs SET status='pending', last_error=?, run_at=?, locked_until=0, updated_at=? WHERE id=?`,
		runErr.Error(), next.Unix(), now.Unix(), id)
}

// Backoff is 5s, 10s, 20s … capped at 30 minutes.
func Backoff(attempt int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= 30*time.Minute {
			return 30 * time.Minute
		}
	}
	return d
}
