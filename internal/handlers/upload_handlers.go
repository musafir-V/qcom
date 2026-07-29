package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

const (
	pickerUploadUseCase    = "picker_photo"
	pickerUploadEntityType = "picker"
)

type UploadHandlers struct {
	uploadService *service.UploadService
	logger        *logrus.Logger
}

func NewUploadHandlers(uploadService *service.UploadService, logger *logrus.Logger) *UploadHandlers {
	return &UploadHandlers{uploadService: uploadService, logger: logger}
}

type GenerateUploadURLRequest struct {
	UseCase  string `json:"use_case"`
	FileName string `json:"file_name"`
	FileType string `json:"file_type"`
	FileSize int64  `json:"file_size"`
}

type InternalPickerUploadURLRequest struct {
	EntityID string `json:"entity_id"`
	FileName string `json:"file_name"`
	FileType string `json:"file_type"`
	FileSize int64  `json:"file_size"`
}

type GenerateUploadURLResponse struct {
	FileID           string `json:"file_id"`
	UploadURL        string `json:"upload_url"`
	ObjectKey        string `json:"object_key"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	MaxFileSize      int64  `json:"max_file_size"`
}

type GenerateViewURLResponse struct {
	ViewURL          string `json:"view_url"`
	ObjectKey        string `json:"object_key"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

// GenerateUploadURL is the generalized, use-case-driven presign endpoint.
func (h *UploadHandlers) GenerateUploadURL(w http.ResponseWriter, r *http.Request) {
	h.generate(w, r, "")
}

// GeneratePrintUploadURL preserves the legacy print route by forcing use_case=print_file.
func (h *UploadHandlers) GeneratePrintUploadURL(w http.ResponseWriter, r *http.Request) {
	h.generate(w, r, "print_file")
}

// GetViewURL returns a short-lived presigned GET URL for an object the caller owns.
func (h *UploadHandlers) GetViewURL(w http.ResponseWriter, r *http.Request) {
	entityID, _ := r.Context().Value("entity_id").(string)
	entityType, _ := r.Context().Value("entity_type").(string)
	if entityID == "" {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Entity ID not found in token")
		return
	}

	useCase := r.URL.Query().Get("use_case")
	objectKey := r.URL.Query().Get("object_key")
	if useCase == "" || objectKey == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "use_case and object_key query params are required")
		return
	}

	result, err := h.uploadService.GeneratePresignedViewURL(r.Context(), useCase, entityType, entityID, objectKey)
	if err != nil {
		status, code := classifyUploadError(err)
		h.logger.WithError(err).WithField("use_case", useCase).Warn("upload view presign failed")
		msg := err.Error()
		if status >= 500 {
			msg = "Something went wrong, please try again"
		}
		h.respondWithError(w, status, code, msg)
		return
	}

	writeJSON(w, h.logger, http.StatusOK, GenerateViewURLResponse{
		ViewURL:          result.ViewURL,
		ObjectKey:        objectKey,
		ExpiresInSeconds: result.ExpiresInSeconds,
	})
}

// GenerateInternalPickerUploadURL is an unauthenticated, service-to-service presign
// endpoint locked to pickers. It forces use_case=picker_photo and entity_type=picker;
// the picker id is supplied by the trusted internal caller (order-service).
func (h *UploadHandlers) GenerateInternalPickerUploadURL(w http.ResponseWriter, r *http.Request) {
	var req InternalPickerUploadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.EntityID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "entity_id is required")
		return
	}

	result, err := h.uploadService.GeneratePresignedURL(r.Context(), pickerUploadUseCase, pickerUploadEntityType, req.EntityID, req.FileName, req.FileType, req.FileSize)
	if err != nil {
		status, code := classifyUploadError(err)
		h.logger.WithError(err).WithField("use_case", pickerUploadUseCase).Warn("upload presign failed")
		msg := err.Error()
		if status >= 500 {
			msg = "Something went wrong, please try again"
		}
		h.respondWithError(w, status, code, msg)
		return
	}

	writeJSON(w, h.logger, http.StatusOK, GenerateUploadURLResponse{
		FileID:           result.FileID,
		UploadURL:        result.UploadURL,
		ObjectKey:        result.ObjectKey,
		ExpiresInSeconds: result.ExpiresInSeconds,
		MaxFileSize:      result.MaxFileSize,
	})
}

// GetInternalPickerViewURL is an unauthenticated, service-to-service presigned GET
// endpoint locked to pickers. It forces use_case=picker_photo and entity_type=picker;
// the picker id is supplied by the trusted internal caller (order-service).
func (h *UploadHandlers) GetInternalPickerViewURL(w http.ResponseWriter, r *http.Request) {
	entityID := r.URL.Query().Get("entity_id")
	objectKey := r.URL.Query().Get("object_key")
	if strings.TrimSpace(entityID) == "" || strings.TrimSpace(objectKey) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "entity_id and object_key query params are required")
		return
	}

	result, err := h.uploadService.GeneratePresignedViewURL(r.Context(), pickerUploadUseCase, pickerUploadEntityType, entityID, objectKey)
	if err != nil {
		status, code := classifyUploadError(err)
		h.logger.WithError(err).WithField("use_case", pickerUploadUseCase).Warn("upload view presign failed")
		msg := err.Error()
		if status >= 500 {
			msg = "Something went wrong, please try again"
		}
		h.respondWithError(w, status, code, msg)
		return
	}

	writeJSON(w, h.logger, http.StatusOK, GenerateViewURLResponse{
		ViewURL:          result.ViewURL,
		ObjectKey:        objectKey,
		ExpiresInSeconds: result.ExpiresInSeconds,
	})
}

func (h *UploadHandlers) generate(w http.ResponseWriter, r *http.Request, forcedUseCase string) {
	entityID, _ := r.Context().Value("entity_id").(string)
	entityType, _ := r.Context().Value("entity_type").(string)
	if entityID == "" {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Entity ID not found in token")
		return
	}

	var req GenerateUploadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	useCase := req.UseCase
	if forcedUseCase != "" {
		useCase = forcedUseCase
	}
	if useCase == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "use_case is required")
		return
	}

	result, err := h.uploadService.GeneratePresignedURL(r.Context(), useCase, entityType, entityID, req.FileName, req.FileType, req.FileSize)
	if err != nil {
		status, code := classifyUploadError(err)
		h.logger.WithError(err).WithField("use_case", useCase).Warn("upload presign failed")
		msg := err.Error()
		if status >= 500 {
			msg = "Something went wrong, please try again"
		}
		h.respondWithError(w, status, code, msg)
		return
	}

	writeJSON(w, h.logger, http.StatusOK, GenerateUploadURLResponse{
		FileID:           result.FileID,
		UploadURL:        result.UploadURL,
		ObjectKey:        result.ObjectKey,
		ExpiresInSeconds: result.ExpiresInSeconds,
		MaxFileSize:      result.MaxFileSize,
	})
}

func classifyUploadError(err error) (int, string) {
	switch {
	case errors.Is(err, service.ErrUnknownUseCase):
		return http.StatusBadRequest, "UNKNOWN_USE_CASE"
	case errors.Is(err, service.ErrEntityTypeNotAllowed):
		return http.StatusForbidden, "ENTITY_TYPE_NOT_ALLOWED"
	case errors.Is(err, service.ErrMimeNotAllowed):
		return http.StatusBadRequest, "MIME_NOT_ALLOWED"
	case errors.Is(err, service.ErrFileTooLarge):
		return http.StatusBadRequest, "FILE_TOO_LARGE"
	case errors.Is(err, service.ErrInvalidFileRequest):
		return http.StatusBadRequest, "INVALID_REQUEST"
	case errors.Is(err, service.ErrInvalidObjectKey):
		return http.StatusBadRequest, "INVALID_OBJECT_KEY"
	default:
		return http.StatusInternalServerError, "PRESIGN_FAILED"
	}
}

func (h *UploadHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, h.logger, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}
