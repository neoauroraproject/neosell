package payments

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"neosell/internal/db"
	"neosell/internal/wallet"
)

func setup(t *testing.T) (*Service, *wallet.Wallet, *db.DB) {
	d, err := db.OpenPath(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for _, id := range []int{1, 2} {
		d.Exec(`INSERT INTO users(id, email, created_at, updated_at) VALUES (?, ?, 0, 0)`, id, "u"+string(rune('0'+id))+"@x.io")
	}
	w := wallet.New(d)
	return New(d, w), w, d
}

func TestWalletLedger(t *testing.T) {
	_, w, d := setup(t)
	ctx := context.Background()
	if _, err := w.Apply(ctx, wallet.Entry{UserID: 1, Amount: -1, Type: "purchase"}); !errors.Is(err, wallet.ErrInsufficient) {
		t.Fatalf("overdraft: %v", err)
	}
	w.Apply(ctx, wallet.Entry{UserID: 1, Amount: 1000, Type: "topup", IdempotencyKey: "k1"})
	w.Apply(ctx, wallet.Entry{UserID: 1, Amount: 1000, Type: "topup", IdempotencyKey: "k1"}) // replay
	if b, _ := w.Balance(ctx, 1); b != 1000 {
		t.Fatalf("idempotent credit: %d", b)
	}
	d.Exec(`UPDATE users SET credit_limit = 500 WHERE id = 1`)
	if _, err := w.Apply(ctx, wallet.Entry{UserID: 1, Amount: -1500, Type: "purchase"}); err != nil {
		t.Fatalf("within credit limit: %v", err)
	}
	if _, err := w.Apply(ctx, wallet.Entry{UserID: 1, Amount: -1, Type: "purchase"}); !errors.Is(err, wallet.ErrInsufficient) {
		t.Fatal("beyond credit limit")
	}
	if _, err := w.Apply(ctx, wallet.Entry{UserID: 1, Amount: -100, Type: "admin", AllowNegative: true}); err != nil {
		t.Fatal(err)
	}
	if bad, _ := w.Verify(ctx); len(bad) != 0 {
		t.Fatalf("ledger mismatch %v", bad)
	}
	h, _ := w.History(ctx, 1, 0, 10)
	if len(h) != 3 || h[0].BalanceAfter != -600 {
		t.Fatalf("history %+v", h)
	}
}

func TestConcurrentDebitsNeverOverdraw(t *testing.T) {
	// A real file in WAL mode: the case that matters in production.
	d, err := db.OpenPath(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.Exec(`INSERT INTO users(id, email, created_at, updated_at) VALUES (1, 'a@x.io', 0, 0)`)
	w := wallet.New(d)
	ctx := context.Background()
	w.Apply(ctx, wallet.Entry{UserID: 1, Amount: 1000, Type: "topup"})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.Apply(ctx, wallet.Entry{UserID: 1, Amount: -100, Type: "purchase"}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if b, _ := w.Balance(ctx, 1); b < 0 || ok != 10 || b != 0 {
		t.Fatalf("balance %d after %d successful debits", b, ok)
	}
}

func TestCardTopupFlow(t *testing.T) {
	s, w, _ := setup(t)
	ctx := context.Background()
	if _, err := s.CreateCardTopup(ctx, 1, 100000, Limits{}); !errors.Is(err, ErrNoCards) {
		t.Fatalf("no cards: %v", err)
	}
	s.SaveCard(ctx, Card{Number: "6037991234567890", Holder: "A", Enabled: true})
	if _, err := s.CreateCardTopup(ctx, 1, 100, Limits{Min: 10000}); !errors.Is(err, ErrAmountBounds) {
		t.Fatal("min not enforced")
	}
	p, err := s.CreateCardTopup(ctx, 1, 100000, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if p.PayAmount <= 100000 || p.PayAmount > 100999 || p.Card == nil || p.Status != StatusPending {
		t.Fatalf("payment %+v", p)
	}
	// Another user cannot attach a receipt to it.
	if _, err := s.AttachReceipt(ctx, 2, p.ID, "x.jpg", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign receipt accepted")
	}
	if p, err = s.AttachReceipt(ctx, 1, p.ID, "r/1.jpg", "paid"); err != nil || p.Status != StatusSubmitted || !p.HasReceipt {
		t.Fatalf("receipt: %v %+v", err, p)
	}
	if _, err := s.Approve(ctx, p.ID, 9, "ok", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, p.ID, 9, "again", 0); !errors.Is(err, ErrBadState) {
		t.Fatal("double approve")
	}
	if b, _ := w.Balance(ctx, 1); b != p.PayAmount {
		t.Fatalf("credited %d want %d", b, p.PayAmount)
	}
	// A new request replaces the old open one; rejected ones cannot be approved.
	p2, _ := s.CreateCardTopup(ctx, 1, 50000, Limits{})
	p3, _ := s.CreateCardTopup(ctx, 1, 60000, Limits{})
	if old, _ := s.Get(ctx, p2.ID); old.Status != StatusExpired {
		t.Fatalf("old request not replaced: %s", old.Status)
	}
	s.Reject(ctx, p3.ID, 9, "fake")
	if _, err := s.Approve(ctx, p3.ID, 9, "", 0); !errors.Is(err, ErrBadState) {
		t.Fatal("approved a rejected payment")
	}
}
