package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"neosell/internal/catalog"
	"neosell/internal/license"
	"neosell/internal/payments"
	"neosell/internal/pricing"
)

const maxReceiptBytes = 5 << 20

func productModule(typ string) string {
	if typ == "outbound" {
		return license.ModOutbound
	}
	return license.ModPorts
}

func (s *Server) userGroup(ctx context.Context, userID int64) int64 {
	var g int64
	s.DB.QueryRowContext(ctx, `SELECT COALESCE(group_id, 0) FROM users WHERE id = ?`, userID).Scan(&g)
	return g
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// writePricingErr: validation errors from the engine are 400, the rest 500.
func writePricingErr(w http.ResponseWriter, err error) {
	var pe pricing.Error
	if errors.As(err, &pe) {
		writeErr(w, 400, pe.Msg)
		return
	}
	writeErr(w, 500, err.Error())
}

// --- shop ---

type shopProduct struct {
	ID          int64              `json:"id"`
	Type        string             `json:"type"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Options     pricing.Options    `json:"options"`
	Locations   []catalog.Location `json:"locations,omitempty"`
}

// GET /api/shop/products — products this customer may buy.
func (s *Server) handleShopProducts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if writeLicenseErr(w, s.License.RequireActive()) {
		return
	}
	u := currentUser(r)
	gid := s.userGroup(ctx, u.ID)
	all, err := s.Catalog.Products(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	locs, _ := s.Catalog.Locations(ctx)
	out := []shopProduct{}
	for _, p := range all {
		if !p.VisibleTo(gid) || !s.License.HasModule(productModule(p.Type)) {
			continue
		}
		sp := shopProduct{ID: p.ID, Type: p.Type, Title: p.Title, Description: p.Description, Options: p.Options}
		if p.Type == "outbound" {
			for _, l := range locs {
				if len(p.Options.Locations) == 0 || containsID(p.Options.Locations, l.ID) {
					sp.Locations = append(sp.Locations, l)
				}
			}
		}
		out = append(out, sp)
	}
	classes, _ := s.Catalog.PortClasses(ctx)
	writeJSON(w, 200, map[string]any{"items": out, "port_classes": classes})
}

func containsID(xs []int64, id int64) bool {
	for _, x := range xs {
		if x == id {
			return true
		}
	}
	return false
}

// quote prices a selection for a customer (shared by the shop and, later, orders).
func (s *Server) quote(ctx context.Context, in *catalog.QuoteInput, c catalog.Customer) (*catalog.Product, *pricing.Quote, error) {
	now := time.Now().Unix()
	p, rules, pctx, err := s.Catalog.Prepare(ctx, in, c, now, s.Settings.Int(ctx, "pricing.round_to"), catalog.CouponUsage{})
	if err != nil {
		return nil, nil, err
	}
	q, err := pricing.Price(p.Pricing(), rules, in.Selection, pctx)
	return p, q, err
}

// POST /api/shop/quote
func (s *Server) handleShopQuote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if writeLicenseErr(w, s.License.RequireActive()) {
		return
	}
	var in catalog.QuoteInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u := currentUser(r)
	gid := s.userGroup(ctx, u.ID)
	p, err := s.Catalog.Product(ctx, in.ProductID)
	if err != nil || !p.VisibleTo(gid) {
		writeErr(w, 404, "محصول پیدا نشد")
		return
	}
	if writeLicenseErr(w, s.License.RequireModule(productModule(p.Type))) {
		return
	}
	_, q, err := s.quote(ctx, &in, catalog.Customer{UserID: u.ID, GroupID: gid})
	if err != nil {
		writePricingErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"quote": q, "selection": in.Selection})
}

// --- wallet (customer) ---

func (s *Server) handleWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := currentUser(r)
	bal, err := s.Wallet.Balance(ctx, u.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	txs, err := s.Wallet.History(ctx, u.ID, before, 30)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"balance": bal, "transactions": txs})
}

func (s *Server) handleTopup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if writeLicenseErr(w, s.License.RequireActive()) {
		return
	}
	if !s.Settings.Bool(ctx, "payments.card.enabled") {
		writeErr(w, 403, "شارژ کارت‌به‌کارت فعال نیست")
		return
	}
	var in struct {
		Amount int64 `json:"amount"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	lim := payments.Limits{Min: s.Settings.Int(ctx, "payments.min_topup"), Max: s.Settings.Int(ctx, "payments.max_topup"),
		TTL: time.Duration(s.Settings.Int(ctx, "payments.card_ttl_hours")) * time.Hour}
	p, err := s.Payments.CreateCardTopup(ctx, currentUser(r).ID, in.Amount, lim)
	if err != nil {
		code := 400
		if !errors.Is(err, payments.ErrAmountBounds) && !errors.Is(err, payments.ErrNoCards) {
			code = 500
		}
		msg := err.Error()
		if errors.Is(err, payments.ErrAmountBounds) {
			msg = fmt.Sprintf("مبلغ باید حداقل %d", lim.Min)
			if lim.Max > 0 {
				msg += fmt.Sprintf(" و حداکثر %d", lim.Max)
			}
			msg += " باشد"
		}
		writeErr(w, code, msg)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) handleMyPayments(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	list, err := s.Payments.List(r.Context(), payments.ListFilter{UserID: currentUser(r).ID, BeforeID: before})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

var receiptTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "application/pdf": ".pdf"}

// POST /api/wallet/payments/{id}/receipt (multipart: file, note)
func (s *Server) handleReceiptUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, maxReceiptBytes+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeErr(w, 400, "حجم فایل حداکثر ۵ مگابایت است")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "تصویر رسید را انتخاب کنید")
		return
	}
	defer f.Close()
	if hdr.Size > maxReceiptBytes {
		writeErr(w, 400, "حجم فایل حداکثر ۵ مگابایت است")
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ext, ok := receiptTypes[http.DetectContentType(head[:n])]
	if !ok {
		writeErr(w, 400, "فقط عکس (JPG/PNG/WEBP) یا PDF")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	rnd := make([]byte, 16)
	rand.Read(rnd)
	rel := filepath.Join("receipts", time.Now().Format("200601"), hex.EncodeToString(rnd)+ext)
	abs := filepath.Join(s.filesDir(), rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := io.Copy(out, io.LimitReader(f, maxReceiptBytes)); err != nil {
		out.Close()
		os.Remove(abs)
		writeErr(w, 500, err.Error())
		return
	}
	out.Close()
	p, err := s.Payments.AttachReceipt(ctx, currentUser(r).ID, pathID(r), filepath.ToSlash(rel), strings.TrimSpace(r.FormValue("note")))
	if err != nil {
		os.Remove(abs)
		code := 400
		if errors.Is(err, payments.ErrNotFound) {
			code = 404
		}
		writeErr(w, code, err.Error())
		return
	}
	s.notifyAdmins(ctx, "payment.submitted", p)
	writeJSON(w, 200, p)
}

func (s *Server) filesDir() string {
	return filepath.Join(s.Cfg.DataDir, "files")
}

// serveReceipt streams a stored receipt; ownerOnly limits it to the payer.
func (s *Server) serveReceipt(w http.ResponseWriter, r *http.Request, ownerOnly bool) {
	rel, uid, err := s.Payments.ReceiptFile(r.Context(), pathID(r))
	if err != nil || rel == "" || (ownerOnly && uid != currentUser(r).ID) {
		writeErr(w, 404, "رسید پیدا نشد")
		return
	}
	// rel is generated by us, but never trust a stored path blindly.
	clean := filepath.Clean(filepath.FromSlash(rel))
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		writeErr(w, 404, "رسید پیدا نشد")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
	http.ServeFile(w, r, filepath.Join(s.filesDir(), clean))
}

func (s *Server) handleMyReceipt(w http.ResponseWriter, r *http.Request) { s.serveReceipt(w, r, true) }

// notifyAdmins is a hook for the Telegram bot / web notifications (next phases).
func (s *Server) notifyAdmins(ctx context.Context, event string, payload any) {
	if s.Notify != nil {
		s.Notify(ctx, event, payload)
	}
}
