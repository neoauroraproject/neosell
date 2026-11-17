// Package notify stores user notifications (shown in the panel) and forwards
// them to Telegram when the user linked their account.
package notify

import (
	"context"
	"log/slog"

	"neosell/internal/db"
)

// Sender delivers a message to a Telegram chat (the bot, when running).
type Sender interface {
	SendText(ctx context.Context, chatID int64, text string) error
}

type Center struct {
	DB     *db.DB
	Sender Sender
}

func (c *Center) Notify(ctx context.Context, userID int64, kind, title, body string) {
	if _, err := c.DB.ExecContext(ctx, `INSERT INTO notifications(user_id, kind, title, body, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, kind, title, body, db.Now()); err != nil {
		slog.Error("notify", "err", err)
	}
	if c.Sender == nil {
		return
	}
	var chat int64
	c.DB.QueryRowContext(ctx, `SELECT COALESCE(telegram_chat_id,0) FROM users WHERE id = ?`, userID).Scan(&chat)
	if chat != 0 {
		go func() {
			if err := c.Sender.SendText(context.Background(), chat, "<b>"+esc(title)+"</b>\n"+esc(body)); err != nil {
				slog.Warn("telegram notify", "err", err)
			}
		}()
	}
}

type Item struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Read      bool   `json:"read"`
	CreatedAt int64  `json:"created_at"`
}

func (c *Center) List(ctx context.Context, userID int64, limit int) ([]Item, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT id, kind, title, body, read_at > 0, created_at FROM notifications WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var i Item
		rows.Scan(&i.ID, &i.Kind, &i.Title, &i.Body, &i.Read, &i.CreatedAt)
		out = append(out, i)
	}
	var unread int
	c.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM notifications WHERE user_id = ? AND read_at = 0`, userID).Scan(&unread)
	return out, unread, rows.Err()
}

func (c *Center) MarkRead(ctx context.Context, userID int64) error {
	_, err := c.DB.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at = 0`, db.Now(), userID)
	return err
}

func esc(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '<':
			out = append(out, []rune("&lt;")...)
		case '>':
			out = append(out, []rune("&gt;")...)
		case '&':
			out = append(out, []rune("&amp;")...)
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
