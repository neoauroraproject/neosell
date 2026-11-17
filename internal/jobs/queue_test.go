package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"neosell/internal/db"
)

func newQ(t *testing.T) (*Queue, *db.DB) {
	d, err := db.OpenPath(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return New(d), d
}

func status(t *testing.T, d *db.DB, id int64) (string, int) {
	var s string
	var a int
	if err := d.QueryRow(`SELECT status, attempts FROM jobs WHERE id=?`, id).Scan(&s, &a); err != nil {
		t.Fatal(err)
	}
	return s, a
}

func TestRunsAndRetries(t *testing.T) {
	q, d := newQ(t)
	calls := 0
	q.Register("x", func(ctx context.Context, p json.RawMessage) error {
		calls++
		if calls == 1 {
			return errors.New("transient")
		}
		return nil
	})
	id, err := q.Enqueue(context.Background(), "x", map[string]int{"a": 1}, Options{})
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	if ran, _ := q.RunOne(context.Background()); !ran {
		t.Fatal("should run")
	}
	if s, a := status(t, d, id); s != "pending" || a != 1 {
		t.Fatalf("after failure: %s %d", s, a)
	}
	// Not due yet (backoff).
	if ran, _ := q.RunOne(context.Background()); ran {
		t.Fatal("should wait for backoff")
	}
	d.Exec(`UPDATE jobs SET run_at = 0 WHERE id = ?`, id)
	q.RunOne(context.Background())
	if s, _ := status(t, d, id); s != "done" {
		t.Fatalf("status %s", s)
	}
}

func TestPermanentAndMaxAttempts(t *testing.T) {
	q, d := newQ(t)
	q.Register("p", func(ctx context.Context, p json.RawMessage) error { return Permanent(errors.New("no")) })
	id, _ := q.Enqueue(context.Background(), "p", nil, Options{})
	q.RunOne(context.Background())
	if s, _ := status(t, d, id); s != "failed" {
		t.Fatalf("status %s", s)
	}
	q.Register("m", func(ctx context.Context, p json.RawMessage) error { return errors.New("again") })
	id2, _ := q.Enqueue(context.Background(), "m", nil, Options{MaxAttempts: 2})
	q.RunOne(context.Background())
	d.Exec(`UPDATE jobs SET run_at = 0 WHERE id = ?`, id2)
	q.RunOne(context.Background())
	if s, a := status(t, d, id2); s != "failed" || a != 2 {
		t.Fatalf("status %s attempts %d", s, a)
	}
}

func TestDedupe(t *testing.T) {
	q, _ := newQ(t)
	q.Register("d", func(ctx context.Context, p json.RawMessage) error { return nil })
	a, _ := q.Enqueue(context.Background(), "d", nil, Options{DedupeKey: "k"})
	b, _ := q.Enqueue(context.Background(), "d", nil, Options{DedupeKey: "k"})
	if a == 0 || b != 0 {
		t.Fatalf("a=%d b=%d", a, b)
	}
	q.RunOne(context.Background())
	c, _ := q.Enqueue(context.Background(), "d", nil, Options{DedupeKey: "k"})
	if c == 0 {
		t.Fatal("finished job should release its key")
	}
}

func TestPanicIsCaught(t *testing.T) {
	q, d := newQ(t)
	q.Register("boom", func(ctx context.Context, p json.RawMessage) error { panic("x") })
	id, _ := q.Enqueue(context.Background(), "boom", nil, Options{MaxAttempts: 1})
	q.RunOne(context.Background())
	if s, _ := status(t, d, id); s != "failed" {
		t.Fatalf("status %s", s)
	}
}

func TestBackoff(t *testing.T) {
	if Backoff(1) != 5*time.Second || Backoff(2) != 10*time.Second || Backoff(30) != 30*time.Minute {
		t.Fatal("backoff curve")
	}
}
