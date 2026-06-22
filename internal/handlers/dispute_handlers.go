// internal/handlers/dispute_handlers.go
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type DisputeHandlers struct {
	disputeService *service.DisputeService
	uploadService  *service.UploadService
	logger         *logrus.Logger
}

func NewDisputeHandlers(disputeService *service.DisputeService, uploadService *service.UploadService, logger *logrus.Logger) *DisputeHandlers {
	return &DisputeHandlers{disputeService: disputeService, uploadService: uploadService, logger: logger}
}

type dispositionDTO struct {
	Code                string `json:"code"`
	Title               string `json:"title"`
	Subtitle            string `json:"subtitle,omitempty"`
	PhotosRequired      bool   `json:"photos_required"`
	PhotoMin            int    `json:"photo_min"`
	DescriptionRequired bool   `json:"description_required"`
}

type createDisputeRequest struct {
	OrderNumber     string   `json:"order_number"`
	DispositionCode string   `json:"disposition_code"`
	Description     string   `json:"description"`
	PhotoKeys       []string `json:"photo_keys"`
}

func (h *DisputeHandlers) ListDispositions(w http.ResponseWriter, r *http.Request) {
	items, err := h.disputeService.ListDispositions(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("failed to list dispositions")
		h.respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load dispositions")
		return
	}
	out := make([]dispositionDTO, 0, len(items))
	for _, d := range items {
		out = append(out, dispositionDTO{
			Code: d.Code, Title: d.Title, Subtitle: d.Subtitle,
			PhotosRequired: d.PhotosRequired, PhotoMin: d.PhotoMin, DescriptionRequired: d.DescriptionRequired,
		})
	}
	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"dispositions": out})
}

func (h *DisputeHandlers) CreateDispute(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.customerID(w, r)
	if !ok {
		return
	}
	var req createDisputeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if req.OrderNumber == "" || req.DispositionCode == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "order_number and disposition_code are required")
		return
	}
	d, err := h.disputeService.CreateDispute(r.Context(), service.CreateDisputeInput{
		CustomerID:      customerID,
		OrderNumber:     req.OrderNumber,
		DispositionCode: req.DispositionCode,
		Description:     req.Description,
		PhotoKeys:       req.PhotoKeys,
	})
	if err != nil {
		status, code := classifyDisputeError(err)
		if status >= 500 {
			h.logger.WithError(err).Error("create dispute failed")
		}
		msg := err.Error()
		if status >= 500 {
			msg = "Something went wrong, please try again"
		}
		h.respondWithError(w, status, code, msg)
		return
	}
	h.respondWithJSON(w, http.StatusCreated, map[string]interface{}{"dispute": h.toDisputeDTO(r.Context(), customerID, d)})
}

func (h *DisputeHandlers) GetDispute(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.customerID(w, r)
	if !ok {
		return
	}
	id := mux.Vars(r)["id"]
	d, err := h.disputeService.GetDispute(r.Context(), customerID, id)
	if err != nil {
		status, code := classifyDisputeError(err)
		msg := err.Error()
		if status >= 500 {
			msg = "Something went wrong, please try again"
		}
		h.respondWithError(w, status, code, msg)
		return
	}
	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"dispute": h.toDisputeDTO(r.Context(), customerID, d)})
}

func (h *DisputeHandlers) GetDisputeByOrder(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.customerID(w, r)
	if !ok {
		return
	}
	orderNumber := r.URL.Query().Get("order_number")
	if orderNumber == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "order_number query param is required")
		return
	}
	d, err := h.disputeService.GetDisputeByOrder(r.Context(), customerID, orderNumber)
	if err != nil {
		status, code := classifyDisputeError(err)
		msg := err.Error()
		if status >= 500 {
			msg = "Something went wrong, please try again"
		}
		h.respondWithError(w, status, code, msg)
		return
	}
	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"dispute": h.toDisputeDTO(r.Context(), customerID, d)})
}

type disputeDTO struct {
	DisputeID       string   `json:"dispute_id"`
	OrderNumber     string   `json:"order_number"`
	DispositionCode string   `json:"disposition_code"`
	Description     string   `json:"description,omitempty"`
	PhotoKeys       []string `json:"photo_keys,omitempty"`
	PhotoURLs       []string `json:"photo_urls,omitempty"`
	Status          string   `json:"status"`
	ResolutionNote  string   `json:"resolution_note,omitempty"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

func (h *DisputeHandlers) toDisputeDTO(ctx context.Context, customerID string, d *models.Dispute) disputeDTO {
	dto := disputeDTO{
		DisputeID: d.DisputeID, OrderNumber: d.OrderNumber, DispositionCode: d.DispositionCode,
		Description: d.Description, PhotoKeys: d.PhotoKeys, Status: string(d.Status),
		ResolutionNote: d.ResolutionNote, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if len(d.PhotoKeys) == 0 || h.uploadService == nil {
		return dto
	}
	urls, err := h.uploadService.GeneratePresignedViewURLs(ctx, "dispute_photo", "customer", customerID, d.PhotoKeys)
	if err != nil {
		h.logger.WithError(err).WithField("dispute_id", d.DisputeID).Warn("failed to presign dispute photo view URLs")
		return dto
	}
	dto.PhotoURLs = urls
	return dto
}

func (h *DisputeHandlers) customerID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, _ := r.Context().Value("entity_id").(string)
	if id == "" {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Entity ID not found in token")
		return "", false
	}
	return id, true
}

func classifyDisputeError(err error) (int, string) {
	switch {
	case errors.Is(err, service.ErrOrderNotFound):
		return http.StatusNotFound, "ORDER_NOT_FOUND"
	case errors.Is(err, service.ErrOrderNotDisputable):
		return http.StatusConflict, "ORDER_NOT_DISPUTABLE"
	case errors.Is(err, service.ErrDispositionNotFound):
		return http.StatusBadRequest, "DISPOSITION_NOT_FOUND"
	case errors.Is(err, service.ErrDescriptionRequired):
		return http.StatusBadRequest, "DESCRIPTION_REQUIRED"
	case errors.Is(err, service.ErrDescriptionTooShort):
		return http.StatusBadRequest, "DESCRIPTION_TOO_SHORT"
	case errors.Is(err, service.ErrDescriptionTooLong):
		return http.StatusBadRequest, "DESCRIPTION_TOO_LONG"
	case errors.Is(err, service.ErrTooManyPhotos):
		return http.StatusBadRequest, "TOO_MANY_PHOTOS"
	case errors.Is(err, service.ErrNotEnoughPhotos):
		return http.StatusBadRequest, "NOT_ENOUGH_PHOTOS"
	case errors.Is(err, service.ErrInvalidPhotoKey):
		return http.StatusBadRequest, "INVALID_PHOTO_KEY"
	case errors.Is(err, service.ErrDisputeAlreadyOpen):
		return http.StatusConflict, "DISPUTE_ALREADY_OPEN"
	case errors.Is(err, service.ErrDisputeNotFound):
		return http.StatusNotFound, "DISPUTE_NOT_FOUND"
	case errors.Is(err, service.ErrDisputeForbidden):
		return http.StatusForbidden, "FORBIDDEN"
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR"
	}
}

func (h *DisputeHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}

func (h *DisputeHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
