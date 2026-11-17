// Package payments: wallet top-ups. This file is card-to-card with a receipt
// that an admin approves; online gateways plug in later behind the
// "gateways" license module.
package payments

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"neosell/internal/db"
	"neosell/internal/wallet"
)

var (
	ErrNoCards      = errors.New("فعلاً شماره کارتی برای واریز ثبت نشده است")
	ErrNotFound     = errors.New("پرداخت پیدا نشد")
	ErrBadState     = errors.New("وضعیت این پرداخت اجازه‌ی این کار را نمی‌دهد")
	ErrAmountBounds = errors.New("مبلغ خارج از محدوده‌ی مجاز است")
)

const (
	StatusPending   = "pending"   // waiting for the customer's receipt
	StatusSubmitted = "submitted" // receipt uploaded, waiting for an admin
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusExpired   = "expired"
)

type Card struct {
	ID      int64  `json:"id"`
	Number  string `json:"number"`
	Holder  string `json:"holder"`
	Bank    string `json:"bank"`
	Enabled bool   `json:"enabled"`
	Sort    int    `json:"sort"`
}

type Payment struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	UserEmail    string `json:"user_email,omitempty"`
	Method       string `json:"method"`
	Amount       int64  `json:"amount"`
	PayAmount    int64  `json:"pay_amount"`
	Status       string `json:"status"`
	Card         *Card  `json:"card,omitempty"`
	HasReceipt   bool   `json:"has_receipt"`
	CustomerNote string `json:"customer_note"`
	AdminNote    string `json:"admin_note"`
	ReviewedBy   int64  `json:"reviewed_by,omitempty"`
	ReviewedAt   int64  `json:"reviewed_at,omitempty"`
	ExpiresAt    int64  `json:"expires_at"`
	CreatedAt    int64  `json:"created_at"`
	receiptFile  string
}

type Limits struct {
	Min, Max int64
	TTL      time.Duration // how long the customer has to pay and upload
}

type Service struct {
	db     *db.DB
	wallet *wallet.Wallet
}

func New(d *db.DB, w *wallet.Wallet) *Service { return &Service{db: d, wallet: w} }

// --- cards (admin) ---

func (s *Service) Cards(ctx context.Context, onlyEnabled bool) ([]Card, error) {
	q := `SELECT id, number, holder, bank, enabled, sort FROM bank_cards`
	if onlyEnabled {
		q += ` WHERE enabled = 1`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Card{}
	for rows.Next() {
		var c Card
		if err := rows.Scan(&c.ID, &c.Number, &c.Holder, &c.Bank, &c.Enabled, &c.Sort); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) SaveCard(ctx context.Context, c Card) (int64, error) {
	if len(c.Number) < 16 {
		return 0, errors.New("شماره کارت نامعتبر است")
	}
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO bank_cards(number, holder, bank, enabled, sort) VALUES (?, ?, ?, ?, ?)`, c.Number, c.Holder, c.Bank, c.Enabled, c.Sort)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE bank_cards SET number=?, holder=?, bank=?, enabled=?, sort=? WHERE id=?`, c.Number, c.Holder, c.Bank, c.Enabled, c.Sort, c.ID)
	return c.ID, err
}

func (s *Service) DeleteCard(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bank_cards SET enabled = 0 WHERE id = ?`, id) // keep history references
	return err
}

// --- top-up flow ---

// CreateCardTopup picks the least-used enabled card and a unique amount
// (amount + 1..999) that no other open payment uses, so an admin can match a
// bank statement line to exactly one request.
func (s *Service) CreateCardTopup(ctx context.Context, userID, amount int64, lim Limits) (*Payment, error) {
	if amount <= 0 || (lim.Min > 0 && amount < lim.Min) || (lim.Max > 0 && amount > lim.Max) {
		return nil, ErrAmountBounds
	}
	if lim.TTL == 0 {
		lim.TTL = 24 * time.Hour
	}
	var id int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		// Only one open request per user: the old one is replaced.
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET status='expired', updated_at=? WHERE user_id=? AND method='card' AND status='pending'`, db.Now(), userID); err != nil {
			return err
		}
		var card Card
		err := tx.QueryRowContext(ctx, `SELECT id, number, holder, bank FROM bank_cards WHERE enabled = 1 ORDER BY used, sort, id LIMIT 1`).
			Scan(&card.ID, &card.Number, &card.Holder, &card.Bank)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoCards
		}
		if err != nil {
			return err
		}
		pay := int64(0)
		for i := 0; i < 50; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(999))
			cand := amount + n.Int64() + 1
			var taken int
			tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM payments WHERE pay_amount = ? AND status IN ('pending','submitted')`, cand).Scan(&taken)
			if taken == 0 {
				pay = cand
				break
			}
		}
		if pay == 0 {
			return errors.New("لطفاً مبلغ دیگری امتحان کنید")
		}
		snap, _ := json.Marshal(card)
		now := db.Now()
		res, err := tx.ExecContext(ctx, `INSERT INTO payments(user_id, method, amount, pay_amount, status, card_id, card_snapshot, expires_at, created_at, updated_at)
			VALUES (?, 'card', ?, ?, 'pending', ?, ?, ?, ?, ?)`, userID, amount, pay, card.ID, string(snap), now+int64(lim.TTL.Seconds()), now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, `UPDATE bank_cards SET used = used + 1 WHERE id = ?`, card.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// AttachReceipt records the stored receipt file; the payment waits for review.
func (s *Service) AttachReceipt(ctx context.Context, userID, paymentID int64, file, note string) (*Payment, error) {
	p, err := s.Get(ctx, paymentID)
	if err != nil || p.UserID != userID {
		return nil, ErrNotFound
	}
	if p.Status != StatusPending && p.Status != StatusSubmitted {
		return nil, ErrBadState
	}
	if p.Status == StatusPending && db.Now() > p.ExpiresAt {
		return nil, errors.New("مهلت این درخواست تمام شده؛ درخواست تازه بسازید")
	}
	if len(note) > 500 {
		note = note[:500]
	}
	_, err = s.db.ExecContext(ctx, `UPDATE payments SET receipt_file=?, customer_note=?, status='submitted', updated_at=? WHERE id=?`, file, note, db.Now(), paymentID)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, paymentID)
}

// Approve credits the wallet with the amount actually paid (pay_amount).
// Approving twice is impossible: the status check and the credit share one
// transaction, and the ledger entry has an idempotency key.
func (s *Service) Approve(ctx context.Context, paymentID, adminID int64, note string, overrideAmount int64) (*Payment, error) {
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var userID, pay int64
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT user_id, pay_amount, status FROM payments WHERE id = ?`, paymentID).Scan(&userID, &pay, &status); err != nil {
			return ErrNotFound
		}
		if status != StatusSubmitted && status != StatusPending {
			return ErrBadState
		}
		credit := pay
		if overrideAmount > 0 {
			credit = overrideAmount
		}
		if _, err := wallet.ApplyTx(ctx, tx, wallet.Entry{UserID: userID, Amount: credit, Type: "topup", RefType: "payment",
			RefID: strconv.FormatInt(paymentID, 10), Note: "شارژ کارت‌به‌کارت", CreatedBy: adminID,
			IdempotencyKey: fmt.Sprintf("payment:%d", paymentID)}); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE payments SET status='approved', admin_note=?, reviewed_by=?, reviewed_at=?, updated_at=? WHERE id=?`,
			note, adminID, db.Now(), db.Now(), paymentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, paymentID)
}

func (s *Service) Reject(ctx context.Context, paymentID, adminID int64, note string) (*Payment, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE payments SET status='rejected', admin_note=?, reviewed_by=?, reviewed_at=?, updated_at=?
		WHERE id=? AND status IN ('pending','submitted')`, note, adminID, db.Now(), db.Now(), paymentID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrBadState
	}
	return s.Get(ctx, paymentID)
}

// ExpireStale closes pending requests whose time ran out (no receipt).
func (s *Service) ExpireStale(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE payments SET status='expired', updated_at=? WHERE status='pending' AND expires_at < ?`, db.Now(), db.Now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const payCols = `p.id, p.user_id, COALESCE(u.email,''), p.method, p.amount, p.pay_amount, p.status, COALESCE(p.card_snapshot,''),
	COALESCE(p.receipt_file,''), p.customer_note, p.admin_note, COALESCE(p.reviewed_by,0), COALESCE(p.reviewed_at,0), p.expires_at, p.created_at`

func scanPay(row interface{ Scan(...any) error }) (*Payment, error) {
	p := &Payment{}
	var snap string
	err := row.Scan(&p.ID, &p.UserID, &p.UserEmail, &p.Method, &p.Amount, &p.PayAmount, &p.Status, &snap,
		&p.receiptFile, &p.CustomerNote, &p.AdminNote, &p.ReviewedBy, &p.ReviewedAt, &p.ExpiresAt, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	if snap != "" {
		var c Card
		if json.Unmarshal([]byte(snap), &c) == nil {
			p.Card = &c
		}
	}
	p.HasReceipt = p.receiptFile != ""
	return p, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Payment, error) {
	p, err := scanPay(s.db.QueryRowContext(ctx, `SELECT `+payCols+` FROM payments p LEFT JOIN users u ON u.id = p.user_id WHERE p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ReceiptFile returns the stored receipt path (relative to the files dir).
func (s *Service) ReceiptFile(ctx context.Context, id int64) (string, int64, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return "", 0, err
	}
	return p.receiptFile, p.UserID, nil
}

type ListFilter struct {
	UserID   int64
	Status   string
	BeforeID int64
	Limit    int
}

func (s *Service) List(ctx context.Context, f ListFilter) ([]Payment, error) {
	q := `SELECT ` + payCols + ` FROM payments p LEFT JOIN users u ON u.id = p.user_id WHERE 1=1`
	args := []any{}
	if f.UserID > 0 {
		q += ` AND p.user_id = ?`
		args = append(args, f.UserID)
	}
	if f.Status != "" {
		q += ` AND p.status = ?`
		args = append(args, f.Status)
	}
	if f.BeforeID > 0 {
		q += ` AND p.id < ?`
		args = append(args, f.BeforeID)
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 30
	}
	q += ` ORDER BY p.id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Payment{}
	for rows.Next() {
		p, err := scanPay(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
