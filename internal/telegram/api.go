// Package telegram is a minimal Telegram Bot API client (no dependencies).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type API struct {
	Token string
	Base  string // default https://api.telegram.org
	HTTP  *http.Client
}

func NewAPI(token, proxy string) *API {
	tr := &http.Transport{}
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &API{Token: token, Base: "https://api.telegram.org", HTTP: &http.Client{Timeout: 70 * time.Second, Transport: tr}}
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

func (a *API) do(ctx context.Context, method string, req *http.Request, out any) error {
	res, err := a.HTTP.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var r apiResp
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: %v", method, err)
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (a *API) Call(ctx context.Context, method string, params any, out any) error {
	b, _ := json.Marshal(params)
	req, _ := http.NewRequest(http.MethodPost, a.Base+"/bot"+a.Token+"/"+method, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return a.do(ctx, method, req, out)
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type PhotoSize struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type Message struct {
	MessageID int64       `json:"message_id"`
	From      *User       `json:"from"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption"`
	Photo     []PhotoSize `json:"photo"`
	Document  *Document   `json:"document"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type InlineKeyboard struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}

type ReplyKeyboard struct {
	Keyboard       [][]map[string]string `json:"keyboard"`
	ResizeKeyboard bool                  `json:"resize_keyboard"`
}

func (a *API) GetMe(ctx context.Context) (*User, error) {
	var u User
	return &u, a.Call(ctx, "getMe", map[string]any{}, &u)
}

func (a *API) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	var ups []Update
	err := a.Call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 50, "allowed_updates": []string{"message", "callback_query"}}, &ups)
	return ups, err
}

func (a *API) Send(ctx context.Context, chatID int64, text string, markup any) (*Message, error) {
	p := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if markup != nil {
		p["reply_markup"] = markup
	}
	var m Message
	return &m, a.Call(ctx, "sendMessage", p, &m)
}

func (a *API) Edit(ctx context.Context, chatID, msgID int64, text string, markup any) error {
	p := map[string]any{"chat_id": chatID, "message_id": msgID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if markup != nil {
		p["reply_markup"] = markup
	}
	return a.Call(ctx, "editMessageText", p, nil)
}

func (a *API) EditCaption(ctx context.Context, chatID, msgID int64, caption string) error {
	return a.Call(ctx, "editMessageCaption", map[string]any{"chat_id": chatID, "message_id": msgID, "caption": caption, "parse_mode": "HTML"}, nil)
}

func (a *API) Answer(ctx context.Context, callbackID, text string) error {
	return a.Call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text}, nil)
}

// SendFile uploads a photo (or document) with a caption and inline buttons.
func (a *API) SendFile(ctx context.Context, chatID int64, asPhoto bool, name string, data []byte, caption string, markup any) error {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	w.WriteField("caption", caption)
	w.WriteField("parse_mode", "HTML")
	if markup != nil {
		m, _ := json.Marshal(markup)
		w.WriteField("reply_markup", string(m))
	}
	field, method := "document", "sendDocument"
	if asPhoto {
		field, method = "photo", "sendPhoto"
	}
	fw, _ := w.CreateFormFile(field, name)
	fw.Write(data)
	w.Close()
	req, _ := http.NewRequest(http.MethodPost, a.Base+"/bot"+a.Token+"/"+method, &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return a.do(ctx, method, req, nil)
}

// Download fetches a file the user sent (max bytes).
func (a *API) Download(ctx context.Context, fileID string, maxBytes int64) ([]byte, error) {
	var f struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := a.Call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	if f.FileSize > maxBytes {
		return nil, errors.New("file too large")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.Base+"/file/bot"+a.Token+"/"+f.FilePath, nil)
	res, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("download: HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if int64(len(data)) > maxBytes {
		return nil, errors.New("file too large")
	}
	return data, err
}
