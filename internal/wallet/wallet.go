// Package wallet is the customer's prepaid balance: an append-only ledger
// (wallet_transactions) plus a cached balance updated in the same transaction.
package wallet

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"neosell/internal/db"
)

var (
	ErrInsufficient = errors.New("موجودی کیف پول کافی نیست")
	ErrZeroAmount   = errors.New("مبلغ نامعتبر است")
)

type Entry struct {
	UserID         int64
	Amount         int64 // + credit, - debit
	Type           string
	RefType        string
	RefID          string
	Note           string
	CreatedBy      int64
	IdempotencyKey string
	// AllowNegative lets an admin adjustment go below zero (beyond credit_limit).
	AllowNegative bool
}

type Tx struct {
	ID           int64  `json:"id"`
	Amount       int64  `json:"amount"`
	Type         string `json:"type"`
	RefType      string `json:"ref_type"`
	RefID        string `json:"ref_id"`
	BalanceAfter int64  `json:"balance_after"`
	Note         string `json:"note"`
	CreatedBy    int64  `json:"created_by,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

type Wallet struct{ db *db.DB }

func New(d *db.DB) *Wallet { return &Wallet{db: d} }

func (w *Wallet) Balance(ctx context.Context, userID int64) (int64, error) {
	var b int64
	err := w.db.QueryRowContext(ctx, `SELECT balance FROM wallets WHERE user_id = ?`, userID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return b, err
}

// Apply posts one entry in its own transaction.
func (w *Wallet) Apply(ctx context.Context, e Entry) (*Tx, error) {
	var out *Tx
	err := w.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = ApplyTx(ctx, tx, e)
		return err
	})
	return out, err
}

// ApplyTx posts an entry inside the caller's transaction (e.g. debit + order
// creation commit together). With an IdempotencyKey, a repeated call returns
// the original transaction instead of posting twice.
func ApplyTx(ctx context.Context, tx *sql.Tx, e Entry) (*Tx, error) {
	if e.Amount == 0 {
		return nil, ErrZeroAmount
	}
	if e.IdempotencyKey != "" {
		if t, err := byKey(ctx, tx, e.IdempotencyKey); err == nil {
			return t, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO wallets(user_id, balance, updated_at) VALUES (?, 0, ?)`, e.UserID, now); err != nil {
		return nil, err
	}
	var bal, creditLimit int64
	if err := tx.QueryRowContext(ctx, `SELECT w.balance, u.credit_limit FROM wallets w JOIN users u ON u.id = w.user_id WHERE w.user_id = ?`, e.UserID).Scan(&bal, &creditLimit); err != nil {
		return nil, err
	}
	next := bal + e.Amount
	if e.Amount < 0 && !e.AllowNegative && next < -creditLimit {
		return nil, ErrInsufficient
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallets SET balance = ?, updated_at = ? WHERE user_id = ?`, next, now, e.UserID); err != nil {
		return nil, err
	}
	var key any
	if e.IdempotencyKey != "" {
		key = e.IdempotencyKey
	}
	var by any
	if e.CreatedBy > 0 {
		by = e.CreatedBy
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO wallet_transactions(user_id, amount, type, ref_type, ref_id, balance_after, note, created_by, idempotency_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.UserID, e.Amount, e.Type, e.RefType, e.RefID, next, strings.TrimSpace(e.Note), by, key, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Tx{ID: id, Amount: e.Amount, Type: e.Type, RefType: e.RefType, RefID: e.RefID, BalanceAfter: next, Note: e.Note, CreatedBy: e.CreatedBy, CreatedAt: now}, nil
}

func byKey(ctx context.Context, tx *sql.Tx, key string) (*Tx, error) {
	t := &Tx{}
	var by sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id, amount, type, COALESCE(ref_type,''), COALESCE(ref_id,''), balance_after, note, created_by, created_at
		FROM wallet_transactions WHERE idempotency_key = ?`, key).
		Scan(&t.ID, &t.Amount, &t.Type, &t.RefType, &t.RefID, &t.BalanceAfter, &t.Note, &by, &t.CreatedAt)
	t.CreatedBy = by.Int64
	return t, err
}

// History lists a user's transactions newest first; beforeID pages backwards.
func (w *Wallet) History(ctx context.Context, userID, beforeID int64, limit int) ([]Tx, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	q := `SELECT id, amount, type, COALESCE(ref_type,''), COALESCE(ref_id,''), balance_after, note, COALESCE(created_by,0), created_at
		FROM wallet_transactions WHERE user_id = ?`
	args := []any{userID}
	if beforeID > 0 {
		q += ` AND id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := w.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tx{}
	for rows.Next() {
		var t Tx
		if err := rows.Scan(&t.ID, &t.Amount, &t.Type, &t.RefType, &t.RefID, &t.BalanceAfter, &t.Note, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Verify recomputes every balance from the ledger; it returns users whose
// cached balance differs (should always be empty).
func (w *Wallet) Verify(ctx context.Context) ([]int64, error) {
	rows, err := w.db.QueryContext(ctx, `SELECT w.user_id FROM wallets w
		WHERE w.balance != COALESCE((SELECT SUM(amount) FROM wallet_transactions t WHERE t.user_id = w.user_id), 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bad []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		bad = append(bad, id)
	}
	return bad, rows.Err()
}
