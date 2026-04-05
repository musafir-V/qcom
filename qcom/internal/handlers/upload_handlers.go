package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type UploadHandlers struct {
	uploadService *service.UploadService
	logger        *logrus.Logger
}

func NewUploadHandlers(uploadService *service.UploadService, logger *logrus.Logger) *UploadHandlers {
	return &UploadHandlers{
		uploadService: uploadService,
		logger:        logger,
	}
}

type GenerateUploadURLRequest struct {
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

func (h *UploadHandlers) ValidateFileRequest(w http.ResponseWriter, r *http.Request) (string, *GenerateUploadURLRequest, bool) {
	userID, ok := r.Context().Value("user_id").(string)
	if !ok || userID == "" {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User ID not found in token")
		return "", nil, false
	}

	var req GenerateUploadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return "", nil, false
	}

	if err := h.uploadService.ValidateFileRequest(req.FileName, req.FileType, req.FileSize); err != nil {
		h.logger.WithError(err).WithField("user_id", userID).Warn("Upload validation failed")
		h.respondWithError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return "", nil, false
	}

	return userID, &req, true
}

func (h *UploadHandlers) GenerateUploadURL(w http.ResponseWriter, r *http.Request) {
	userID, req, ok := h.ValidateFileRequest(w, r)
	if !ok {
		return
	}

	result, err := h.uploadService.GeneratePresignedURL(r.Context(), userID, req.FileName, req.FileType, req.FileSize)
	if err != nil {
		h.logger.WithError(err).WithField("user_id", userID).Error("Failed to generate presigned URL")
		h.respondWithError(w, http.StatusInternalServerError, "PRESIGN_FAILED", "Failed to generate upload URL")
		return
	}

	h.logger.WithFields(logrus.Fields{
		"user_id":    userID,
		"file_id":    result.FileID,
		"object_key": result.ObjectKey,
	}).Info("Presigned upload URL generated")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(GenerateUploadURLResponse{
		FileID:           result.FileID,
		UploadURL:        result.UploadURL,
		ObjectKey:        result.ObjectKey,
		ExpiresInSeconds: result.ExpiresInSeconds,
		MaxFileSize:      result.MaxFileSize,
	})
}

func (h *UploadHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}
