package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type HomeHandlers struct {
	pageRepo *repository.PageRepository
	logger   *logrus.Logger
}

func NewHomeHandlers(
	pageRepo *repository.PageRepository,
	logger *logrus.Logger,
) *HomeHandlers {
	return &HomeHandlers{
		pageRepo: pageRepo,
		logger:   logger,
	}
}

type HomeRequest struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type HomeResponse struct {
	Data map[string]interface{} `json:"data"`
}

func (h *HomeHandlers) GetHome(w http.ResponseWriter, r *http.Request) {
	var req HomeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.WithError(err).Error("Failed to decode home request")
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	// Log the received lat/long (not using it for now as per requirements)
	h.logger.WithFields(logrus.Fields{
		"latitude":  req.Latitude,
		"longitude": req.Longitude,
	}).Info("Received home request with location")

	// Query DynamoDB for PAGE#HOME
	pageData, err := h.pageRepo.GetPageByKey(r.Context(), "PAGE#HOME")
	if err != nil {
		h.logger.WithError(err).Error("Failed to fetch home page data")
		respondWithError(w, http.StatusInternalServerError, "FETCH_FAILED", "Failed to fetch home page data")
		return
	}

	if pageData == nil {
		respondWithError(w, http.StatusNotFound, "PAGE_NOT_FOUND", "Home page data not found")
		return
	}

	// Return the page data
	respondWithJSON(w, http.StatusOK, HomeResponse{
		Data: pageData,
	})
}
