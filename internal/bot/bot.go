// Package bot is the Telegram bot: customers see services, buy packages,
// renew, top up their wallet with a receipt photo; admins approve receipts.
package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"neosell/internal/auth"
	"neosell/internal/catalog"
	"neosell/internal/db"
	"neosell/internal/license"
	"neosell/internal/payments"
	"neosell/internal/pricing"
	"neosell/internal/rbac"
	"neosell/internal/services"
	"neosell/internal/settings"
	"neosell/internal/telegram"
	"neosell/internal/wallet"
)

type Bot struct {
	DB       *db.DB
	Users    *auth.Store
	RBAC     *rbac.Store
	Settings *settings.Store
	Wallet   *wallet.Wallet
	Payments *payments.Service
	Catalog  *catalog.Store
	Services *services.Manager
	License  *license.Manager
	FilesDir string
	Proxy    string
	// PublicURL returns the panel URL (settings may change at runtime).
	PublicURL func() string
	// OnPayment runs after an admin decides on a receipt from Telegram.
	OnPayment func(ctx context.Context, p *payments.Payment, approved bool)

	mu       sync.RWMutex
	api      *telegram.API
	username string
	states   sync.Map // chatID → *state
}

type state struct {
	step      string // topup_amount | topup_receipt
	paymentID int64
	at        time.Time
}

// SendText implements notify.Sender.
func (b *Bot) SendText(ctx context.Context, chatID int64, text string) error {
	api := b.current()
	if api == nil {
		return errors.New("bot not running")
	}
	_, err := api.Send(ctx, chatID, text, nil)
	return err
}

func (b *Bot) current() *telegram.API {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.api
}

// Username is the bot's @name once running (for deep links).
func (b *Bot) Username() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.username
}

// Run polls Telegram while the bot is enabled and licensed; it follows token
// and on/off changes from the settings without a restart.
func (b *Bot) Run(ctx context.Context) {
	for ctx.Err() == nil {
		token := b.Settings.String(ctx, "bot.token")
		if !b.Settings.Bool(ctx, "bot.enabled") || token == "" || !b.License.HasModule(license.ModBot) {
			b.setAPI(nil, "")
			sleep(ctx, 20*time.Second)
			continue
		}
		api := telegram.NewAPI(token, b.Proxy)
		me, err := api.GetMe(ctx)
		if err != nil {
			slog.Warn("telegram: getMe failed", "err", err)
			sleep(ctx, 30*time.Second)
			continue
		}
		b.setAPI(api, me.Username)
		slog.Info("telegram bot running", "username", me.Username)
		b.poll(ctx, api, token)
	}
}

func (b *Bot) setAPI(api *telegram.API, user string) {
	b.mu.Lock()
	b.api, b.username = api, user
	b.mu.Unlock()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (b *Bot) poll(ctx context.Context, api *telegram.API, token string) {
	var offset int64
	for ctx.Err() == nil {
		if b.Settings.String(ctx, "bot.token") != token || !b.Settings.Bool(ctx, "bot.enabled") {
			return
		}
		ups, err := api.GetUpdates(ctx, offset)
		if err != nil {
			slog.Warn("telegram: getUpdates", "err", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("telegram handler panic", "err", r)
					}
				}()
				hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()
				b.handle(hctx, api, u)
			}()
		}
	}
}

// ---------- texts & keyboards ----------

const (
	btnServices = "📦 سرویس‌های من"
	btnBuy      = "🛒 خرید"
	btnWallet   = "💳 کیف پول"
	btnPanel    = "🌐 ورود به پنل"
	btnSupport  = "🎫 پشتیبانی"
)

func mainMenu() telegram.ReplyKeyboard {
	row := func(xs ...string) []map[string]string {
		out := []map[string]string{}
		for _, x := range xs {
			out = append(out, map[string]string{"text": x})
		}
		return out
	}
	return telegram.ReplyKeyboard{ResizeKeyboard: true, Keyboard: [][]map[string]string{row(btnServices, btnBuy), row(btnWallet, btnPanel), row(btnSupport)}}
}

func inline(rows ...[]telegram.Button) telegram.InlineKeyboard {
	return telegram.InlineKeyboard{InlineKeyboard: rows}
}

func cb(text, data string) telegram.Button { return telegram.Button{Text: text, CallbackData: data} }

func money(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func gbs(b int64) string { return strconv.FormatFloat(float64(b)/(1<<30), 'f', 2, 64) + " GB" }

func bar(used, total int64) string {
	if total <= 0 {
		return "نامحدود"
	}
	n := int(used * 10 / total)
	if n > 10 {
		n = 10
	}
	return strings.Repeat("▰", n) + strings.Repeat("▱", 10-n) + fmt.Sprintf(" %d٪", used*100/total)
}

var statusFa = map[string]string{
	"provisioning": "⏳ در حال ساخت", "waiting_peer": "⏳ منتظر اتصال سرور خارج", "active": "✅ فعال", "limited": "⛔ حجم تمام شد",
	"expired": "⌛ منقضی", "paused": "⏸ متوقف", "suspended": "🚫 معلق", "failed": "❌ ناموفق",
}

// ---------- dispatch ----------

func (b *Bot) handle(ctx context.Context, api *telegram.API, u telegram.Update) {
	if q := u.CallbackQuery; q != nil {
		b.onCallback(ctx, api, q)
		return
	}
	m := u.Message
	if m == nil || m.From == nil || m.Chat.Type != "private" {
		return
	}
	chat := m.Chat.ID
	text := strings.TrimSpace(m.Text)

	if strings.HasPrefix(text, "/start") {
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/start"))
		if strings.HasPrefix(arg, "link_") {
			uid, err := b.Users.UseToken(ctx, arg, "link_")
			if err != nil {
				api.Send(ctx, chat, "⛔ لینک اتصال نامعتبر یا منقضی است. از پنل دوباره بسازید.", nil)
				return
			}
			b.Users.LinkTelegram(ctx, uid, chat, m.From.Username)
			api.Send(ctx, chat, "✅ حساب شما به تلگرام وصل شد.", mainMenu())
			return
		}
	}
	user, err := b.Users.ByTelegram(ctx, chat)
	if err != nil {
		b.welcomeGuest(ctx, api, chat)
		return
	}
	if user.Status == "blocked" {
		api.Send(ctx, chat, "⛔ حساب شما مسدود است.", nil)
		return
	}

	// Conversation steps.
	if v, ok := b.states.Load(chat); ok {
		st := v.(*state)
		if time.Since(st.at) > 30*time.Minute {
			b.states.Delete(chat)
		} else if b.onStep(ctx, api, user, m, st) {
			return
		}
	}

	switch text {
	case "/start", "/menu":
		api.Send(ctx, chat, html.EscapeString(b.Settings.String(ctx, "bot.welcome")), mainMenu())
	case btnServices:
		b.showServices(ctx, api, user, chat, 0)
	case btnBuy:
		b.showProducts(ctx, api, user, chat, 0)
	case btnWallet:
		b.showWallet(ctx, api, user, chat, 0)
	case btnPanel:
		b.sendLogin(ctx, api, user, chat)
	case btnSupport:
		sup := b.Settings.String(ctx, "site.support")
		if sup == "" {
			sup = "از بخش تیکت در پنل وب پیام بدهید."
		}
		api.Send(ctx, chat, "🎫 پشتیبانی: "+html.EscapeString(sup), nil)
	default:
		api.Send(ctx, chat, "از منوی پایین انتخاب کنید.", mainMenu())
	}
}

func (b *Bot) welcomeGuest(ctx context.Context, api *telegram.API, chat int64) {
	msg := "👋 خوش آمدید.\nبرای استفاده، یک حساب بسازید یا حساب پنل وب خود را از بخش «اتصال تلگرام» وصل کنید."
	var rows [][]telegram.Button
	if b.Settings.String(ctx, "registration.mode") == "open" && b.License.RequireActive() == nil {
		rows = append(rows, []telegram.Button{cb("🆕 ساخت حساب", "signup")})
	}
	if u := b.PublicURL(); strings.HasPrefix(u, "https://") {
		rows = append(rows, []telegram.Button{{Text: "🌐 پنل وب", URL: u}})
	}
	api.Send(ctx, chat, msg, inline(rows...))
}

func (b *Bot) sendLogin(ctx context.Context, api *telegram.API, user *auth.User, chat int64) {
	base := b.PublicURL()
	if base == "" {
		api.Send(ctx, chat, "آدرس پنل وب هنوز تنظیم نشده است.", nil)
		return
	}
	tok, err := b.Users.OneTimeToken(ctx, user.ID, "login_", 300)
	if err != nil {
		return
	}
	link := base + "/api/auth/telegram-login?token=" + tok
	api.Send(ctx, chat, "🔐 لینک ورود یک‌بار مصرف (۵ دقیقه):", inline([]telegram.Button{{Text: "ورود به پنل", URL: link}}))
}

// ---------- services ----------

func (b *Bot) showServices(ctx context.Context, api *telegram.API, user *auth.User, chat, editMsg int64) {
	list, _ := b.Services.Services(ctx, services.ListFilter{UserID: user.ID, Limit: 30})
	if len(list) == 0 {
		api.Send(ctx, chat, "هنوز سرویسی ندارید. از «🛒 خرید» شروع کنید.", nil)
		return
	}
	var rows [][]telegram.Button
	for _, s := range list {
		rows = append(rows, []telegram.Button{cb(fmt.Sprintf("#%d %s — %s", s.ID, s.Title, statusFa[s.Status]), fmt.Sprintf("svc:%d", s.ID))})
	}
	text := "📦 سرویس‌های شما:"
	if editMsg > 0 {
		api.Edit(ctx, chat, editMsg, text, inline(rows...))
		return
	}
	api.Send(ctx, chat, text, inline(rows...))
}

func (b *Bot) serviceText(s *services.ServiceInfo) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>#%d %s</b>\nوضعیت: %s\n", s.ID, html.EscapeString(s.Title), statusFa[s.Status])
	if s.ExpiresAt > 0 {
		left := (s.ExpiresAt - time.Now().Unix()) / 86400
		fmt.Fprintf(&sb, "انقضا: %s (%d روز مانده)\n", time.Unix(s.ExpiresAt, 0).Format("2006-01-02"), max(left, 0))
	}
	if s.Type == "port" {
		used := s.UsedUp + s.UsedDown
		fmt.Fprintf(&sb, "مصرف: %s از %s\n%s\n", gbs(used), map[bool]string{true: "نامحدود", false: gbs(s.QuotaBytes)}[s.QuotaBytes == 0], bar(used, s.QuotaBytes))
		for _, p := range s.Ports {
			fmt.Fprintf(&sb, "🔌 <code>%s:%d</code> → %d\n", p.Host, p.Port, p.ToPort)
		}
		if s.Tunnel != nil && s.Tunnel.InstallCmd != "" {
			fmt.Fprintf(&sb, "\nدستور نصب روی سرور خارج:\n<code>%s</code>\n", html.EscapeString(s.Tunnel.InstallCmd))
		}
	} else {
		for _, l := range s.Locations {
			used := l.UsedUp + l.UsedDown
			fmt.Fprintf(&sb, "\n%s %s — %s\n%s / %s %s\n<code>%s</code>\n", l.Flag, html.EscapeString(l.Name), statusFa[l.Status], gbs(used),
				map[bool]string{true: "نامحدود", false: gbs(l.QuotaBytes)}[l.QuotaBytes == 0], bar(used, l.QuotaBytes), l.SubURL)
		}
		fmt.Fprintf(&sb, "\nساب همه‌ی لوکیشن‌ها:\n<code>%s</code>", s.SubURL)
	}
	return sb.String()
}

func (b *Bot) showService(ctx context.Context, api *telegram.API, user *auth.User, chat, msg, id int64) {
	s, err := b.Services.ServiceFor(ctx, id, user.ID)
	if err != nil {
		api.Send(ctx, chat, "سرویس پیدا نشد.", nil)
		return
	}
	rows := [][]telegram.Button{{cb("🔄 به‌روزرسانی", fmt.Sprintf("svc:%d", id)), cb("♻️ تمدید", fmt.Sprintf("renew:%d", id))}, {cb("↩️ بازگشت", "svcs")}}
	api.Edit(ctx, chat, msg, b.serviceText(s), inline(rows...))
}

// ---------- shop (packages) ----------

func (b *Bot) showProducts(ctx context.Context, api *telegram.API, user *auth.User, chat, editMsg int64) {
	if b.License.RequireActive() != nil {
		api.Send(ctx, chat, "فروش موقتاً متوقف است.", nil)
		return
	}
	all, _ := b.Catalog.Products(ctx)
	gid := b.group(ctx, user.ID)
	var rows [][]telegram.Button
	custom := false
	for _, p := range all {
		mod := license.ModPorts
		if p.Type == "outbound" {
			mod = license.ModOutbound
		}
		if !p.VisibleTo(gid) || !b.License.HasModule(mod) {
			continue
		}
		for _, pk := range p.Options.Packages {
			rows = append(rows, []telegram.Button{cb(fmt.Sprintf("%s — %s", p.Title, pk.Name), fmt.Sprintf("pkg:%d:%s", p.ID, pk.ID))})
		}
		for _, m := range p.Options.Models {
			if m != pricing.ModelPackage {
				custom = true
			}
		}
	}
	text := "🛒 یک بسته انتخاب کنید:"
	if len(rows) == 0 {
		text = "بسته‌ی آماده‌ای برای فروش نیست."
	}
	if custom && strings.HasPrefix(b.PublicURL(), "http") {
		text += "\nبرای خرید سفارشی (انتخاب پورت، حجم و لوکیشن) از پنل وب استفاده کنید."
		rows = append(rows, []telegram.Button{{Text: "🌐 خرید سفارشی در پنل", URL: b.PublicURL() + "/shop"}})
	}
	if editMsg > 0 {
		api.Edit(ctx, chat, editMsg, text, inline(rows...))
		return
	}
	api.Send(ctx, chat, text, inline(rows...))
}

func (b *Bot) group(ctx context.Context, uid int64) int64 {
	var g int64
	b.DB.QueryRowContext(ctx, `SELECT COALESCE(group_id,0) FROM users WHERE id = ?`, uid).Scan(&g)
	return g
}

func quoteText(q *pricing.Quote) string {
	var sb strings.Builder
	for _, l := range q.Lines {
		fmt.Fprintf(&sb, "• %s: %s\n", html.EscapeString(l.Label), money(l.Amount))
	}
	for _, l := range q.Adjustments {
		fmt.Fprintf(&sb, "• %s: %s\n", html.EscapeString(l.Label), money(l.Amount))
	}
	fmt.Fprintf(&sb, "<b>مبلغ نهایی: %s</b>", money(q.Total))
	return sb.String()
}

// ---------- wallet ----------

func (b *Bot) showWallet(ctx context.Context, api *telegram.API, user *auth.User, chat, editMsg int64) {
	bal, _ := b.Wallet.Balance(ctx, user.ID)
	text := fmt.Sprintf("💳 موجودی کیف پول: <b>%s</b>", money(bal))
	var rows [][]telegram.Button
	if b.Settings.Bool(ctx, "payments.card.enabled") {
		rows = append(rows, []telegram.Button{cb("➕ شارژ کارت‌به‌کارت", "topup")})
	}
	if editMsg > 0 {
		api.Edit(ctx, chat, editMsg, text, inline(rows...))
		return
	}
	api.Send(ctx, chat, text, inline(rows...))
}

func (b *Bot) onStep(ctx context.Context, api *telegram.API, user *auth.User, m *telegram.Message, st *state) bool {
	chat := m.Chat.ID
	switch st.step {
	case "topup_amount":
		if strings.HasPrefix(m.Text, "/") || m.Text == btnServices || m.Text == btnBuy || m.Text == btnWallet || m.Text == btnPanel || m.Text == btnSupport {
			b.states.Delete(chat)
			return false
		}
		amount, err := strconv.ParseInt(strings.ReplaceAll(strings.ReplaceAll(faDigits(m.Text), ",", ""), "٬", ""), 10, 64)
		if err != nil || amount <= 0 {
			api.Send(ctx, chat, "مبلغ را فقط با عدد بفرستید (مثلاً 200000).", nil)
			return true
		}
		lim := payments.Limits{Min: b.Settings.Int(ctx, "payments.min_topup"), Max: b.Settings.Int(ctx, "payments.max_topup"),
			TTL: time.Duration(b.Settings.Int(ctx, "payments.card_ttl_hours")) * time.Hour}
		p, err := b.Payments.CreateCardTopup(ctx, user.ID, amount, lim)
		if err != nil {
			msg := err.Error()
			if errors.Is(err, payments.ErrAmountBounds) {
				msg = fmt.Sprintf("مبلغ باید حداقل %s باشد", money(lim.Min))
				if lim.Max > 0 {
					msg += " و حداکثر " + money(lim.Max)
				}
			}
			api.Send(ctx, chat, "⛔ "+msg, nil)
			return true
		}
		b.states.Store(chat, &state{step: "topup_receipt", paymentID: p.ID, at: time.Now()})
		api.Send(ctx, chat, fmt.Sprintf("لطفاً دقیقاً <b>%s</b> تومان به کارت زیر واریز کنید:\n\n<code>%s</code>\n%s %s\n\nسپس <b>عکس رسید</b> را همین‌جا بفرستید.",
			money(p.PayAmount), p.Card.Number, html.EscapeString(p.Card.Holder), html.EscapeString(p.Card.Bank)), nil)
		return true
	case "topup_receipt":
		var fileID string
		if len(m.Photo) > 0 {
			fileID = m.Photo[len(m.Photo)-1].FileID
		} else if m.Document != nil {
			fileID = m.Document.FileID
		}
		if fileID == "" {
			if strings.HasPrefix(m.Text, "/") || m.Text != "" && strings.ContainsAny(m.Text, "📦🛒💳🌐🎫") {
				b.states.Delete(chat)
				return false
			}
			api.Send(ctx, chat, "عکس رسید را بفرستید.", nil)
			return true
		}
		data, err := b.current().Download(ctx, fileID, payments.MaxReceiptBytes)
		if err != nil {
			api.Send(ctx, chat, "⛔ دریافت فایل ناموفق بود (حداکثر ۵ مگابایت).", nil)
			return true
		}
		rel, err := payments.StoreReceipt(b.FilesDir, data)
		if err != nil {
			api.Send(ctx, chat, "⛔ "+err.Error(), nil)
			return true
		}
		p, err := b.Payments.AttachReceipt(ctx, user.ID, st.paymentID, rel, strings.TrimSpace(m.Caption))
		if err != nil {
			os.Remove(filepath.Join(b.FilesDir, rel))
			api.Send(ctx, chat, "⛔ "+err.Error(), nil)
			b.states.Delete(chat)
			return true
		}
		b.states.Delete(chat)
		api.Send(ctx, chat, "✅ رسید ثبت شد. پس از بررسی، کیف پول شارژ می‌شود.", mainMenu())
		b.NotifyAdminsPayment(ctx, p)
		return true
	}
	return false
}

func faDigits(s string) string {
	r := strings.NewReplacer("۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9",
		"٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4", "٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9")
	return strings.TrimSpace(r.Replace(s))
}

// NotifyAdminsPayment sends a submitted receipt to every admin who may
// approve payments and linked Telegram, with approve/reject buttons.
func (b *Bot) NotifyAdminsPayment(ctx context.Context, p *payments.Payment) {
	api := b.current()
	if api == nil || p == nil {
		return
	}
	rel, _, err := b.Payments.ReceiptFile(ctx, p.ID)
	if err != nil {
		return
	}
	data, _ := os.ReadFile(filepath.Join(b.FilesDir, filepath.FromSlash(rel)))
	caption := fmt.Sprintf("🧾 رسید جدید #%d\nکاربر: %s\nمبلغ واریزی: <b>%s</b>\nکارت: %s", p.ID, html.EscapeString(p.UserEmail), money(p.PayAmount), cardOf(p))
	if p.CustomerNote != "" {
		caption += "\nیادداشت: " + html.EscapeString(p.CustomerNote)
	}
	kb := inline([]telegram.Button{cb("✅ تأیید", fmt.Sprintf("pay:ok:%d", p.ID)), cb("❌ رد", fmt.Sprintf("pay:no:%d", p.ID))})
	rows, err := b.DB.QueryContext(ctx, `SELECT id FROM users WHERE is_admin = 1 AND status = 'active' AND telegram_chat_id IS NOT NULL`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		u, err := b.Users.ByID(ctx, id)
		if err != nil || !b.RBAC.Can(ctx, u, rbac.PaymentsApprove) || (!u.IsOwner && !b.License.HasModule(license.ModMultiAdmin)) {
			continue
		}
		if len(data) > 0 {
			if err := api.SendFile(ctx, u.TelegramChatID, payments.IsImage(rel), filepath.Base(rel), data, caption, kb); err != nil {
				slog.Warn("telegram: send receipt", "err", err)
			}
		} else {
			api.Send(ctx, u.TelegramChatID, caption, kb)
		}
	}
}

func cardOf(p *payments.Payment) string {
	if p.Card == nil {
		return "-"
	}
	n := p.Card.Number
	if len(n) > 4 {
		n = "…" + n[len(n)-4:]
	}
	return n
}

// ---------- callbacks ----------

func (b *Bot) onCallback(ctx context.Context, api *telegram.API, q *telegram.CallbackQuery) {
	if q.Message == nil {
		return
	}
	chat, msg := q.Message.Chat.ID, q.Message.MessageID
	parts := strings.Split(q.Data, ":")

	if q.Data == "signup" {
		if _, err := b.Users.ByTelegram(ctx, chat); err == nil {
			api.Answer(ctx, q.ID, "حساب شما از قبل وجود دارد")
			return
		}
		if b.Settings.String(ctx, "registration.mode") != "open" || b.License.RequireActive() != nil {
			api.Answer(ctx, q.ID, "ثبت‌نام بسته است")
			return
		}
		if _, err := b.Users.CreateTelegramUser(ctx, chat, q.From.Username, q.From.FirstName); err != nil {
			api.Answer(ctx, q.ID, "خطا در ساخت حساب")
			return
		}
		api.Answer(ctx, q.ID, "")
		api.Send(ctx, chat, "✅ حساب شما ساخته شد.\n"+html.EscapeString(b.Settings.String(ctx, "bot.welcome")), mainMenu())
		return
	}
	user, err := b.Users.ByTelegram(ctx, chat)
	if err != nil || user.Status == "blocked" {
		api.Answer(ctx, q.ID, "ابتدا حساب بسازید یا وصل کنید")
		return
	}
	id := func(i int) int64 {
		if len(parts) <= i {
			return 0
		}
		n, _ := strconv.ParseInt(parts[i], 10, 64)
		return n
	}
	switch parts[0] {
	case "svcs":
		api.Answer(ctx, q.ID, "")
		b.showServices(ctx, api, user, chat, msg)
	case "svc":
		api.Answer(ctx, q.ID, "")
		b.showService(ctx, api, user, chat, msg, id(1))
	case "renew":
		sid := id(1)
		req := services.OrderRequest{Kind: "renew", ServiceID: sid}
		_, qt, err := b.Services.Quote(ctx, user.ID, &req)
		if err != nil {
			api.Answer(ctx, q.ID, short(err))
			return
		}
		api.Answer(ctx, q.ID, "")
		api.Edit(ctx, chat, msg, "♻️ تمدید سرویس #"+strconv.FormatInt(sid, 10)+"\n"+quoteText(qt),
			inline([]telegram.Button{cb("✅ پرداخت از کیف پول", fmt.Sprintf("renewok:%d:%d", sid, qt.Total))}, []telegram.Button{cb("↩️ بازگشت", fmt.Sprintf("svc:%d", sid))}))
	case "renewok":
		b.placeOrder(ctx, api, q, user, services.OrderRequest{Kind: "renew", ServiceID: id(1), IdempotencyKey: fmt.Sprintf("tg:%d:%d", chat, msg)}, id(2))
	case "pkg":
		pid := id(1)
		pkgID := strings.Join(parts[2:], ":")
		req := services.OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: pricing.ModelPackage, PackageID: pkgID}}
		p, qt, err := b.Services.Quote(ctx, user.ID, &req)
		if err != nil {
			api.Answer(ctx, q.ID, short(err))
			return
		}
		api.Answer(ctx, q.ID, "")
		bal, _ := b.Wallet.Balance(ctx, user.ID)
		text := fmt.Sprintf("🛒 %s\n%s\nموجودی شما: %s", html.EscapeString(p.Title), quoteText(qt), money(bal))
		api.Edit(ctx, chat, msg, text, inline([]telegram.Button{cb("✅ خرید با کیف پول", fmt.Sprintf("buy:%d:%d:%s", pid, qt.Total, pkgID))}, []telegram.Button{cb("↩️ بازگشت", "shop")}))
	case "buy":
		pkgID := strings.Join(parts[3:], ":")
		b.placeOrder(ctx, api, q, user, services.OrderRequest{ProductID: id(1), IdempotencyKey: fmt.Sprintf("tg:%d:%d", chat, msg),
			Selection: pricing.Selection{Model: pricing.ModelPackage, PackageID: pkgID}}, id(2))
	case "shop":
		api.Answer(ctx, q.ID, "")
		b.showProducts(ctx, api, user, chat, msg)
	case "topup":
		api.Answer(ctx, q.ID, "")
		b.states.Store(chat, &state{step: "topup_amount", at: time.Now()})
		api.Send(ctx, chat, fmt.Sprintf("مبلغ شارژ را به تومان بفرستید (حداقل %s):", money(b.Settings.Int(ctx, "payments.min_topup"))), nil)
	case "pay":
		b.onPaymentDecision(ctx, api, q, user, parts)
	default:
		api.Answer(ctx, q.ID, "")
	}
}

func short(err error) string {
	s := err.Error()
	if len([]rune(s)) > 180 {
		s = string([]rune(s)[:180])
	}
	return s
}

func (b *Bot) placeOrder(ctx context.Context, api *telegram.API, q *telegram.CallbackQuery, user *auth.User, req services.OrderRequest, shownTotal int64) {
	chat, msg := q.Message.Chat.ID, q.Message.MessageID
	// Price must still be what the customer saw.
	check := req
	if _, qt, err := b.Services.Quote(ctx, user.ID, &check); err != nil || qt.Total != shownTotal {
		api.Answer(ctx, q.ID, "قیمت تغییر کرده؛ دوباره انتخاب کنید")
		return
	}
	o, err := b.Services.PlaceOrder(ctx, user.ID, req)
	if err != nil {
		var oe services.OrderError
		if errors.As(err, &oe) || errors.Is(err, license.ErrLocked) {
			api.Answer(ctx, q.ID, short(err))
			bal, _ := b.Wallet.Balance(ctx, user.ID)
			if bal < shownTotal {
				api.Send(ctx, chat, "موجودی کافی نیست. از «💳 کیف پول» شارژ کنید.", nil)
			}
			return
		}
		slog.Error("bot order", "err", err)
		api.Answer(ctx, q.ID, "خطا؛ دوباره امتحان کنید")
		return
	}
	api.Answer(ctx, q.ID, "✅ انجام شد")
	s, err := b.Services.ServiceFor(ctx, o.ServiceID, user.ID)
	if err != nil {
		api.Edit(ctx, chat, msg, "✅ سفارش ثبت شد.", nil)
		return
	}
	api.Edit(ctx, chat, msg, "✅ سفارش ثبت شد.\n\n"+b.serviceText(s), inline([]telegram.Button{cb("📦 جزئیات سرویس", fmt.Sprintf("svc:%d", s.ID))}))
}

func (b *Bot) onPaymentDecision(ctx context.Context, api *telegram.API, q *telegram.CallbackQuery, user *auth.User, parts []string) {
	if len(parts) != 3 || !b.RBAC.Can(ctx, user, rbac.PaymentsApprove) || (!user.IsOwner && !b.License.HasModule(license.ModMultiAdmin)) {
		api.Answer(ctx, q.ID, "دسترسی ندارید")
		return
	}
	pid, _ := strconv.ParseInt(parts[2], 10, 64)
	var p *payments.Payment
	var err error
	verdict := ""
	if parts[1] == "ok" {
		p, err = b.Payments.Approve(ctx, pid, user.ID, "تأیید از تلگرام", 0)
		verdict = "✅ تأیید شد توسط " + html.EscapeString(user.Email+user.TelegramUsername)
	} else {
		p, err = b.Payments.Reject(ctx, pid, user.ID, "رد از تلگرام")
		verdict = "❌ رد شد توسط " + html.EscapeString(user.Email+user.TelegramUsername)
	}
	if err != nil {
		api.Answer(ctx, q.ID, short(err))
		return
	}
	api.Answer(ctx, q.ID, "انجام شد")
	api.EditCaption(ctx, q.Message.Chat.ID, q.Message.MessageID, fmt.Sprintf("🧾 رسید #%d — %s\n%s", p.ID, money(p.PayAmount), verdict))
	if b.OnPayment != nil {
		b.OnPayment(ctx, p, parts[1] == "ok")
	}
}
