package handlers

import (
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type QRHandlers struct {
	svc    *service.MarketingQRService
	logger *logrus.Logger
}

func NewQRHandlers(svc *service.MarketingQRService, logger *logrus.Logger) *QRHandlers {
	return &QRHandlers{svc: svc, logger: logger}
}

// Redirect is the public scan endpoint: GET /q/{slug}
func (h *QRHandlers) Redirect(w http.ResponseWriter, r *http.Request) {
	slug := mux.Vars(r)["slug"]
	cookieName := "bq_" + slug
	var cookieVal string
	if c, err := r.Cookie(cookieName); err == nil {
		cookieVal = c.Value
	}

	dest, setUnique := h.svc.HandleScan(r.Context(), slug, r.UserAgent(), cookieVal)
	if setUnique {
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    "1",
			Path:     "/",
			MaxAge:   int((30 * 24 * time.Hour).Seconds()),
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	// Prevent intermediary caching of the redirect so counts stay accurate.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, dest, http.StatusFound)
}

// --- Admin: campaigns ---

func (h *QRHandlers) CreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Name == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_NAME", "name is required")
		return
	}
	c, err := h.svc.CreateCampaign(r.Context(), req.Name, req.Description)
	if err != nil {
		h.logger.WithError(err).Error("qr: create campaign failed")
		respondWithError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create campaign")
		return
	}
	respondWithJSON(w, http.StatusCreated, c)
}

func (h *QRHandlers) ListCampaigns(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListCampaigns(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("qr: list campaigns failed")
		respondWithError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list campaigns")
		return
	}
	if list == nil {
		list = []*models.QRCampaign{}
	}
	respondWithJSON(w, http.StatusOK, list)
}

type placementView struct {
	*models.QRPlacement
	URL string `json:"url"`
}

type campaignDetailResponse struct {
	Campaign   *models.QRCampaign `json:"campaign"`
	Placements []placementView    `json:"placements"`
}

func (h *QRHandlers) GetCampaign(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["campaignId"]
	c, placements, err := h.svc.GetCampaignWithPlacements(r.Context(), id)
	if err != nil {
		h.logger.WithError(err).Error("qr: get campaign failed")
		respondWithError(w, http.StatusInternalServerError, "GET_FAILED", "Failed to get campaign")
		return
	}
	if c == nil {
		respondWithError(w, http.StatusNotFound, "NOT_FOUND", "Campaign not found")
		return
	}
	views := make([]placementView, 0, len(placements))
	for _, p := range placements {
		views = append(views, placementView{QRPlacement: p, URL: h.svc.PlacementURL(p.Slug)})
	}
	respondWithJSON(w, http.StatusOK, campaignDetailResponse{Campaign: c, Placements: views})
}

func (h *QRHandlers) UpdateCampaign(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["campaignId"]
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Enabled     *bool   `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	c, err := h.svc.UpdateCampaign(r.Context(), id, req.Name, req.Description, req.Enabled)
	if err != nil {
		h.logger.WithError(err).Error("qr: update campaign failed")
		respondWithError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update campaign")
		return
	}
	if c == nil {
		respondWithError(w, http.StatusNotFound, "NOT_FOUND", "Campaign not found")
		return
	}
	respondWithJSON(w, http.StatusOK, c)
}

// --- Admin: placements ---

func (h *QRHandlers) AddPlacement(w http.ResponseWriter, r *http.Request) {
	campaignID := mux.Vars(r)["campaignId"]
	var req struct {
		Name     string `json:"name"`
		Location string `json:"location"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Name == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_NAME", "name is required")
		return
	}
	p, err := h.svc.AddPlacement(r.Context(), campaignID, req.Name, req.Location)
	if err != nil {
		h.logger.WithError(err).Error("qr: add placement failed")
		respondWithError(w, http.StatusInternalServerError, "ADD_FAILED", "Failed to add placement")
		return
	}
	if p == nil {
		respondWithError(w, http.StatusNotFound, "NOT_FOUND", "Campaign not found")
		return
	}
	respondWithJSON(w, http.StatusCreated, placementView{QRPlacement: p, URL: h.svc.PlacementURL(p.Slug)})
}

func (h *QRHandlers) UpdatePlacement(w http.ResponseWriter, r *http.Request) {
	slug := mux.Vars(r)["slug"]
	var req struct {
		Name     *string `json:"name"`
		Location *string `json:"location"`
		Enabled  *bool   `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	p, err := h.svc.UpdatePlacement(r.Context(), slug, req.Name, req.Location, req.Enabled)
	if err != nil {
		h.logger.WithError(err).Error("qr: update placement failed")
		respondWithError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update placement")
		return
	}
	if p == nil {
		respondWithError(w, http.StatusNotFound, "NOT_FOUND", "Placement not found")
		return
	}
	respondWithJSON(w, http.StatusOK, placementView{QRPlacement: p, URL: h.svc.PlacementURL(p.Slug)})
}

func (h *QRHandlers) Analytics(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["campaignId"]
	now := time.Now().UTC()
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = now.AddDate(0, 0, -30).Format(time.RFC3339)
	}
	if to == "" {
		to = now.Format(time.RFC3339)
	}
	res, err := h.svc.Analytics(r.Context(), id, from, to)
	if err != nil {
		h.logger.WithError(err).Error("qr: analytics failed")
		respondWithError(w, http.StatusInternalServerError, "ANALYTICS_FAILED", "Failed to load analytics")
		return
	}
	if res == nil {
		respondWithError(w, http.StatusNotFound, "NOT_FOUND", "Campaign not found")
		return
	}
	respondWithJSON(w, http.StatusOK, res)
}
