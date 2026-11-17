package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"

	"neosell/internal/db"
)

const SessionTTL = 30 * 24 * time.Hour

type User struct {
	ID               int64  `json:"id"`
	Email            string `json:"email"`
	Name             string `json:"name"`
	Phone            string `json:"phone,omitempty"`
	IsAdmin          bool   `json:"is_admin"`
	IsOwner          bool   `json:"is_owner"`
	Status           string `json:"status"`
	TelegramChatID   int64  `json:"telegram_chat_id,omitempty"`
	TelegramUsername string `json:"telegram_username,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	passwordHash     string
}

var (
	ErrInvalidLogin = errors.New("ایمیل یا رمز عبور اشتباه است")
	ErrBlocked      = errors.New("حساب شما مسدود شده است")
	ErrEmailTaken   = errors.New("این ایمیل قبلاً ثبت شده است")
	ErrBadEmail     = errors.New("ایمیل معتبر نیست")
)

type Store struct{ db *db.DB }

func NewStore(d *db.DB) *Store { return &Store{db: d} }

const userCols = `id, COALESCE(email,''), name, COALESCE(phone,''), is_admin, is_owner, status,
	COALESCE(telegram_chat_id,0), COALESCE(telegram_username,''), created_at, password_hash`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Phone, &u.IsAdmin, &u.IsOwner, &u.Status,
		&u.TelegramChatID, &u.TelegramUsername, &u.CreatedAt, &u.passwordHash)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func NormalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e {
		return "", ErrBadEmail
	}
	return e, nil
}

func (s *Store) HasOwner(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM users WHERE is_owner = 1`).Scan(&n)
	return n > 0, err
}

type NewUser struct {
	Email, Name, Password string
	IsAdmin, IsOwner      bool
	ReferredBy            int64
}

func (s *Store) Create(ctx context.Context, nu NewUser) (*User, error) {
	email, err := NormalizeEmail(nu.Email)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(nu.Password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(nu.Password)
	if err != nil {
		return nil, err
	}
	now := db.Now()
	var ref any
	if nu.ReferredBy > 0 {
		ref = nu.ReferredBy
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO users(email, name, password_hash, is_admin, is_owner, referral_code, referred_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		email, strings.TrimSpace(nu.Name), hash, nu.IsAdmin || nu.IsOwner, nu.IsOwner, randomCode(8), ref, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.ByID(ctx, id)
}

func (s *Store) ByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) ByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))))
}

// Authenticate checks credentials. It always runs one Argon2 hash so timing
// does not reveal whether the email exists.
func (s *Store) Authenticate(ctx context.Context, email, pw string) (*User, error) {
	u, err := s.ByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		CheckPassword(dummyHash, pw)
		return nil, ErrInvalidLogin
	}
	if err != nil {
		return nil, err
	}
	if !CheckPassword(u.passwordHash, pw) {
		return nil, ErrInvalidLogin
	}
	if u.Status == "blocked" {
		return nil, ErrBlocked
	}
	return u, nil
}

var dummyHash, _ = HashPassword("dummy-password-for-timing")

func (s *Store) SetPassword(ctx context.Context, userID int64, pw string) error {
	if err := ValidatePassword(pw); err != nil {
		return err
	}
	hash, err := HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, db.Now(), userID)
	if err == nil {
		// Changing the password signs out every other session.
		_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	}
	return err
}

// --- sessions ---

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomCode(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}

// CreateSession returns the raw token for the cookie; only its hash is stored.
func (s *Store) CreateSession(ctx context.Context, userID int64, ip, ua string) (string, error) {
	tok := randomToken()
	now := time.Now()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, ip, user_agent, created_at, expires_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, hashToken(tok), userID, ip, ua, now.Unix(), now.Add(SessionTTL).Unix(), now.Unix())
	return tok, err
}

func (s *Store) SessionUser(ctx context.Context, tok string) (*User, error) {
	if tok == "" {
		return nil, sql.ErrNoRows
	}
	h := hashToken(tok)
	now := time.Now().Unix()
	var uid, lastSeen int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, last_seen FROM sessions WHERE token_hash = ? AND expires_at > ?`, h, now).Scan(&uid, &lastSeen)
	if err != nil {
		return nil, err
	}
	u, err := s.ByID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if u.Status == "blocked" {
		return nil, ErrBlocked
	}
	if now-lastSeen > 300 { // avoid a write on every request
		s.db.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE token_hash = ?`, now, h)
	}
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tok string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(tok))
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	return err
}

// CreateTelegramUser registers a customer straight from the Telegram bot
// (no email/password yet; they can sign in to the web panel with a one-time link).
func (s *Store) CreateTelegramUser(ctx context.Context, chatID int64, username, name string) (*User, error) {
	now := db.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users(name, telegram_chat_id, telegram_username, referral_code, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, strings.TrimSpace(name), chatID, username, randomCode(8), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.ByID(ctx, id)
}

// ByTelegram finds the user linked to a Telegram chat.
func (s *Store) ByTelegram(ctx context.Context, chatID int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE telegram_chat_id = ?`, chatID))
}

// LinkTelegram attaches a chat to a user (moving it away from any other account).
func (s *Store) LinkTelegram(ctx context.Context, userID, chatID int64, username string) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET telegram_chat_id = NULL WHERE telegram_chat_id = ? AND id != ?`, chatID, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE users SET telegram_chat_id = ?, telegram_username = ?, updated_at = ? WHERE id = ?`, chatID, username, db.Now(), userID)
		return err
	})
}

func (s *Store) UnlinkTelegram(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET telegram_chat_id = NULL, telegram_username = NULL WHERE id = ?`, userID)
	return err
}

// OneTimeToken stores a short-lived token (Telegram link or login) for a user.
func (s *Store) OneTimeToken(ctx context.Context, userID int64, prefix string, ttl int64) (string, error) {
	tok := prefix + randomToken()[:32]
	_, err := s.db.ExecContext(ctx, `INSERT INTO telegram_links(token, user_id, expires_at) VALUES (?, ?, ?)`, tok, userID, db.Now()+ttl)
	return tok, err
}

// UseToken consumes a one-time token and returns its user.
func (s *Store) UseToken(ctx context.Context, tok, prefix string) (int64, error) {
	if !strings.HasPrefix(tok, prefix) {
		return 0, sql.ErrNoRows
	}
	var uid int64
	err := s.db.QueryRowContext(ctx, `DELETE FROM telegram_links WHERE token = ? AND expires_at > ? RETURNING user_id`, tok, db.Now()).Scan(&uid)
	return uid, err
}
