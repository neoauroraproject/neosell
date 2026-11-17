// Package settings stores admin-editable settings as JSON values in SQLite.
// Every key has a default here, so a fresh install works without any setup,
// and the admin panel can list and edit them all.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"neosell/internal/db"
)

type Kind string

const (
	KString Kind = "string"
	KText   Kind = "text"
	KBool   Kind = "bool"
	KInt    Kind = "int"
	KEnum   Kind = "enum"
	// KSecret values are encrypted at rest and never returned (only "set or not").
	KSecret Kind = "secret"
)

// Sealer encrypts secret settings (secret.Box).
type Sealer interface {
	SealString(string) (string, error)
	OpenString(string) (string, error)
}

// SecretMask is what the API shows for a secret that is set.
const SecretMask = "••••••"

type Def struct {
	Key     string   `json:"key"`
	Group   string   `json:"group"`
	Label   string   `json:"label"`
	Kind    Kind     `json:"kind"`
	Default any      `json:"default"`
	Options []string `json:"options,omitempty"`
	Help    string   `json:"help,omitempty"`
	Public  bool     `json:"-"` // exposed to customers (branding etc.)
}

// Defs is the single source of truth for global settings. Later phases add
// their own keys here (payments, bot, messages …).
var Defs = []Def{
	{Key: "site.name", Group: "general", Label: "نام فروشگاه", Kind: KString, Default: "Neo Sell", Public: true},
	{Key: "site.support", Group: "general", Label: "آیدی یا لینک پشتیبانی", Kind: KString, Default: "", Public: true},
	{Key: "site.rules", Group: "general", Label: "قوانین (نمایش هنگام ثبت‌نام)", Kind: KText, Default: "", Public: true},
	{Key: "site.currency", Group: "general", Label: "واحد پول", Kind: KEnum, Default: "toman", Options: []string{"toman", "usd", "usdt"}, Public: true},
	{Key: "registration.mode", Group: "users", Label: "ثبت‌نام", Kind: KEnum, Default: "open", Options: []string{"open", "closed", "invite"}, Public: true},
	{Key: "usage.gb_base", Group: "usage", Label: "هر گیگابایت چند بایت است", Kind: KEnum, Default: "1073741824", Options: []string{"1073741824", "1000000000"}, Public: true},
	{Key: "lifecycle.suspend_after_days", Group: "lifecycle", Label: "تعلیق سرویس منقضی/تمام‌شده بعد از (روز)", Kind: KInt, Default: 3},
	{Key: "lifecycle.delete_after_days", Group: "lifecycle", Label: "حذف سرویس معلق بعد از (روز)", Kind: KInt, Default: 7},
	{Key: "lifecycle.peer_wait_hours", Group: "lifecycle", Label: "مهلت اتصال سرور خارج مشتری (ساعت)", Kind: KInt, Default: 72},
	{Key: "bot.enabled", Group: "bot", Label: "ربات تلگرام فعال باشد", Kind: KBool, Default: false},
	{Key: "bot.token", Group: "bot", Label: "توکن ربات (از BotFather)", Kind: KSecret, Default: ""},
	{Key: "bot.welcome", Group: "bot", Label: "پیام خوش‌آمد ربات", Kind: KText, Default: "به فروشگاه خوش آمدید 👋 از منوی پایین انتخاب کنید."},
	{Key: "panel.public_url", Group: "general", Label: "آدرس عمومی پنل (برای لینک‌ها، ساب و اسکریپت نصب)", Kind: KString, Default: ""},
	{Key: "notify.usage_thresholds", Group: "notify", Label: "آستانه‌های هشدار حجم (درصد، با کاما)", Kind: KString, Default: "50,80,95"},
	{Key: "payments.card.enabled", Group: "payments", Label: "شارژ کارت‌به‌کارت فعال باشد", Kind: KBool, Default: true, Public: true},
	{Key: "payments.min_topup", Group: "payments", Label: "حداقل مبلغ شارژ", Kind: KInt, Default: 10000, Public: true},
	{Key: "payments.max_topup", Group: "payments", Label: "حداکثر مبلغ شارژ (۰ = بدون سقف)", Kind: KInt, Default: 0, Public: true},
	{Key: "payments.card_ttl_hours", Group: "payments", Label: "مهلت واریز و ارسال رسید (ساعت)", Kind: KInt, Default: 24},
	{Key: "pricing.round_to", Group: "pricing", Label: "گرد کردن قیمت نهایی به مضرب (۰ = بدون گرد کردن)", Kind: KInt, Default: 0},
	{Key: "notify.expiry_days", Group: "notify", Label: "هشدار انقضا (روز مانده، با کاما)", Kind: KString, Default: "3,1"},
}

var byKey = func() map[string]Def {
	m := map[string]Def{}
	for _, d := range Defs {
		m[d.Key] = d
	}
	return m
}()

var ErrUnknownKey = errors.New("تنظیم ناشناخته")

type Store struct {
	Box   Sealer
	db    *db.DB
	mu    sync.RWMutex
	cache map[string]json.RawMessage
}

func NewStore(d *db.DB) *Store { return &Store{db: d} }

func (s *Store) load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	c := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		c[k] = json.RawMessage(v)
	}
	s.cache = c
	return rows.Err()
}

// Get returns the stored value or the default.
func (s *Store) Get(ctx context.Context, key string) (any, error) {
	d, ok := byKey[key]
	if !ok {
		return nil, ErrUnknownKey
	}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	raw, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		return d.Default, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return d.Default, nil
	}
	if d.Kind == KSecret {
		enc, _ := v.(string)
		if enc == "" || s.Box == nil {
			return "", nil
		}
		plain, err := s.Box.OpenString(enc)
		if err != nil {
			return "", nil
		}
		return plain, nil
	}
	return v, nil
}

func (s *Store) String(ctx context.Context, key string) string {
	v, _ := s.Get(ctx, key)
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func (s *Store) Int(ctx context.Context, key string) int64 {
	v, _ := s.Get(ctx, key)
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	}
	return 0
}

func (s *Store) Bool(ctx context.Context, key string) bool {
	v, _ := s.Get(ctx, key)
	b, _ := v.(bool)
	return b
}

// Validate coerces v to the key's kind.
func Validate(key string, v any) (any, error) {
	d, ok := byKey[key]
	if !ok {
		return nil, ErrUnknownKey
	}
	switch d.Kind {
	case KString, KText, KSecret:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: باید متن باشد", d.Label)
		}
		if d.Kind == KString && len(s) > 500 {
			return nil, fmt.Errorf("%s: خیلی طولانی است", d.Label)
		}
		return s, nil
	case KBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s: باید بله/خیر باشد", d.Label)
		}
		return b, nil
	case KInt:
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) || f < 0 {
			return nil, fmt.Errorf("%s: باید عدد صحیح نامنفی باشد", d.Label)
		}
		return int64(f), nil
	case KEnum:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: مقدار نامعتبر", d.Label)
		}
		for _, o := range d.Options {
			if o == s {
				return s, nil
			}
		}
		return nil, fmt.Errorf("%s: مقدار نامعتبر", d.Label)
	}
	return nil, ErrUnknownKey
}

// SetMany validates all values first, then writes them in one transaction.
func (s *Store) SetMany(ctx context.Context, vals map[string]any) (map[string]any, error) {
	clean := map[string]any{}
	for k, v := range vals {
		cv, err := Validate(k, v)
		if err != nil {
			return nil, err
		}
		if byKey[k].Kind == KSecret {
			str := cv.(string)
			if str == SecretMask {
				continue // unchanged
			}
			if str != "" {
				if s.Box == nil {
					return nil, errors.New("secret storage unavailable")
				}
				if str, err = s.Box.SealString(str); err != nil {
					return nil, err
				}
			}
			cv = str
		}
		clean[k] = cv
	}
	now := db.Now()
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range clean {
			raw, _ := json.Marshal(v)
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, k, string(raw), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
	return clean, nil
}

// All returns every setting with its current value.
func (s *Store) All(ctx context.Context, publicOnly bool) (map[string]any, error) {
	out := map[string]any{}
	for _, d := range Defs {
		if publicOnly && !d.Public {
			continue
		}
		v, err := s.Get(ctx, d.Key)
		if err != nil {
			return nil, err
		}
		if d.Kind == KSecret {
			if v != "" {
				v = SecretMask
			}
		}
		out[d.Key] = v
	}
	return out, nil
}
