package httpapi

import (
	"net/http"

	"neosell/internal/catalog"
	"neosell/internal/license"
	"neosell/internal/pricing"
)

type publicProduct struct {
	ID          int64              `json:"id"`
	Type        string             `json:"type"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Models      []pricing.Model    `json:"models"`
	Packages    []pricing.Package  `json:"packages"`
	Locations   []catalog.Location `json:"locations,omitempty"`
	Cores       []string           `json:"cores,omitempty"`
	FromPrice   int64              `json:"from_price"` // cheapest package (0 = custom pricing only)
}

// GET /api/public/catalog — what the landing page shows to visitors: products
// open to everyone (no group restriction) and their ready-made packages.
func (s *Server) handlePublicCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := map[string]any{"products": []publicProduct{}, "locations": []catalog.Location{}}
	if s.License.Status().State != license.StateActive || s.Catalog == nil {
		writeJSON(w, 200, out)
		return
	}
	all, err := s.Catalog.Products(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	locs, _ := s.Catalog.Locations(ctx)
	list := []publicProduct{}
	for _, p := range all {
		if !p.VisibleTo(0) || len(p.VisibleGroups) > 0 || !s.License.HasModule(productModule(p.Type)) {
			continue
		}
		pp := publicProduct{ID: p.ID, Type: p.Type, Title: p.Title, Description: p.Description, Models: p.Options.Models,
			Packages: p.Options.Packages, Cores: p.Options.Cores}
		if pp.Packages == nil {
			pp.Packages = []pricing.Package{}
		}
		for _, pk := range p.Options.Packages {
			if pp.FromPrice == 0 || pk.Price < pp.FromPrice {
				pp.FromPrice = pk.Price
			}
		}
		if p.Type == "outbound" {
			for _, l := range locs {
				if len(p.Options.Locations) == 0 || containsID(p.Options.Locations, l.ID) {
					pp.Locations = append(pp.Locations, l)
				}
			}
		}
		list = append(list, pp)
	}
	out["products"], out["locations"] = list, locs
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, 200, out)
}
