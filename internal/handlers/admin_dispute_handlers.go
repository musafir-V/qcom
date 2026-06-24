package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type AdminDisputeHandlers struct {
	service       *service.AdminDisputeService
	uploadService *service.UploadService
	logger        *logrus.Logger
}

func NewAdminDisputeHandlers(svc *service.AdminDisputeService, uploadService *service.UploadService, logger *logrus.Logger) *AdminDisputeHandlers {
	return &AdminDisputeHandlers{service: svc, uploadService: uploadService, logger: logger}
}

type adminDisputeDTO struct {
	DisputeID        string   `json:"dispute_id"`
	OrderNumber      string   `json:"order_number"`
	CustomerID       string   `json:"customer_id"`
	DispositionCode  string   `json:"disposition_code"`
	DispositionTitle string   `json:"disposition_title,omitempty"`
	Description      string   `json:"description,omitempty"`
	PhotoURLs        []string `json:"photo_urls,omitempty"`
	Status           string   `json:"status"`
	ResolutionNote   string   `json:"resolution_note,omitempty"`
	ResolvedBy       string   `json:"resolved_by,omitempty"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

func (h *AdminDisputeHandlers) toDTO(ctx context.Context, d *models.Dispute, titles map[string]string) adminDisputeDTO {
	dto := adminDisputeDTO{
		DisputeID: d.DisputeID, OrderNumber: d.OrderNumber, CustomerID: d.CustomerID,
		DispositionCode: d.DispositionCode, DispositionTitle: titles[d.DispositionCode],
		Description: d.Description, Status: string(d.Status), ResolutionNote: d.ResolutionNote,
		ResolvedBy: d.ResolvedBy, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if len(d.PhotoKeys) > 0 && h.uploadService != nil {
		urls, err := h.uploadService.GeneratePresignedViewURLs(ctx, "dispute_photo", "customer", d.CustomerID, d.PhotoKeys)
		if err != nil {
			h.logger.WithError(err).WithField("dispute_id", d.DisputeID).Warn("failed to presign dispute photos")
		} else {
			dto.PhotoURLs = urls
		}
	}
	return dto
}

func (h *AdminDisputeHandlers) List(w http.ResponseWriter, r *http.Request) {
	status := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("status")))
	if status == "" {
		status = string(models.DisputeStatusOpen)
	}
	cursor := r.URL.Query().Get("cursor")
	limit := int32(50)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = int32(n)
		}
	}
	disputes, next, err := h.service.ListByStatus(r.Context(), models.DisputeStatus(status), cursor, limit)
	if err != nil {
		h.respondErr(w, err)
		return
	}
	codes := make([]string, 0, len(disputes))
	for _, d := range disputes {
		codes = append(codes, d.DispositionCode)
	}
	titles := h.service.TitlesFor(r.Context(), codes)
	out := make([]adminDisputeDTO, 0, len(disputes))
	for i := range disputes {
		out = append(out, h.toDTO(r.Context(), &disputes[i], titles))
	}
	h.respondJSON(w, http.StatusOK, map[string]interface{}{"disputes": out, "next_cursor": next})
}

func (h *AdminDisputeHandlers) Summary(w http.ResponseWriter, r *http.Request) {
	sum, err := h.service.Summary(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("dispute summary failed")
		h.respondJSON(w, http.StatusInternalServerError, ErrorResponse{Error: ErrorDetail{Code: "INTERNAL_ERROR", Message: "Failed to load dispute summary"}})
		return
	}
	h.respondJSON(w, http.StatusOK, sum)
}

func (h *AdminDisputeHandlers) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	actor, _ := r.Context().Value("entity_id").(string)
	id := mux.Vars(r)["id"]
	var req struct {
		Status         string `json:"status"`
		ResolutionNote string `json:"resolution_note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondJSON(w, http.StatusBadRequest, ErrorResponse{Error: ErrorDetail{Code: "INVALID_REQUEST", Message: "Invalid request body"}})
		return
	}
	newStatus := models.DisputeStatus(strings.ToUpper(strings.TrimSpace(req.Status)))
	d, err := h.service.UpdateStatus(r.Context(), id, newStatus, req.ResolutionNote, actor)
	if err != nil {
		h.respondErr(w, err)
		return
	}
	titles := h.service.TitlesFor(r.Context(), []string{d.DispositionCode})
	h.respondJSON(w, http.StatusOK, map[string]interface{}{"dispute": h.toDTO(r.Context(), d, titles)})
}

func classifyAdminDisputeError(err error) (int, string) {
	switch {
	case errors.Is(err, service.ErrInvalidDisputeStatus):
		return http.StatusBadRequest, "INVALID_STATUS"
	case errors.Is(err, service.ErrInvalidDisputeTransition):
		return http.StatusConflict, "INVALID_TRANSITION"
	case errors.Is(err, service.ErrResolutionNoteRequired):
		return http.StatusBadRequest, "RESOLUTION_NOTE_REQUIRED"
	case errors.Is(err, service.ErrDisputeNotFound):
		return http.StatusNotFound, "DISPUTE_NOT_FOUND"
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR"
	}
}

func (h *AdminDisputeHandlers) respondErr(w http.ResponseWriter, err error) {
	status, code := classifyAdminDisputeError(err)
	msg := err.Error()
	if status >= 500 {
		h.logger.WithError(err).Error("admin dispute request failed")
		msg = "Something went wrong, please try again"
	}
	h.respondJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: msg}})
}

func (h *AdminDisputeHandlers) respondJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
