package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type AddressHandlers struct {
	addressService *service.AddressService
	logger         *logrus.Logger
}

func NewAddressHandlers(addressService *service.AddressService, logger *logrus.Logger) *AddressHandlers {
	return &AddressHandlers{
		addressService: addressService,
		logger:         logger,
	}
}

type CreateAddressRequest struct {
	ReceiverName     string  `json:"receiver_name"`
	ReceiverPhone    string  `json:"receiver_phone"`
	BuildingAndFloor string  `json:"building_and_floor"`
	AddressLine1     string  `json:"address_line_1"`
	AddressLine2     string  `json:"address_line_2"`
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
	Label            string  `json:"label"`
}

type UpdateReceiverRequest struct {
	ReceiverName  *string `json:"receiver_name"`
	ReceiverPhone *string `json:"receiver_phone"`
}

var phoneRegex = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)
var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var validLabels = map[string]bool{"home": true, "work": true, "other": true}

func (h *AddressHandlers) CreateAddress(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)

	var req CreateAddressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	if req.ReceiverName == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "receiver_name is required")
		return
	}
	if len(req.ReceiverName) > 128 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "receiver_name must be at most 128 characters")
		return
	}
	if req.ReceiverPhone == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "receiver_phone is required")
		return
	}
	if !phoneRegex.MatchString(req.ReceiverPhone) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid receiver phone number format")
		return
	}
	if req.BuildingAndFloor == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "building_and_floor is required")
		return
	}
	if len(req.BuildingAndFloor) > 256 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "building_and_floor must be at most 256 characters")
		return
	}
	if req.AddressLine1 == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "address_line_1 is required")
		return
	}
	if len(req.AddressLine1) > 256 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "address_line_1 must be at most 256 characters")
		return
	}
	if len(req.AddressLine2) > 256 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "address_line_2 must be at most 256 characters")
		return
	}
	if req.Latitude < -90 || req.Latitude > 90 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Latitude must be between -90 and 90")
		return
	}
	if req.Longitude < -180 || req.Longitude > 180 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Longitude must be between -180 and 180")
		return
	}
	if req.Label != "" && !validLabels[req.Label] {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_LABEL", "Label must be one of: home, work, other")
		return
	}

	addr := &models.Address{
		ReceiverName:     req.ReceiverName,
		ReceiverPhone:    req.ReceiverPhone,
		BuildingAndFloor: req.BuildingAndFloor,
		AddressLine1:     req.AddressLine1,
		AddressLine2:     req.AddressLine2,
		Latitude:         req.Latitude,
		Longitude:        req.Longitude,
		Label:            req.Label,
	}

	created, err := h.addressService.CreateAddress(r.Context(), userID, addr)
	if err != nil {
		h.logger.WithError(err).Error("Failed to create address")
		h.respondWithError(w, http.StatusInternalServerError, "ADDRESS_CREATION_FAILED", "Failed to create address")
		return
	}

	h.respondWithJSON(w, http.StatusCreated, map[string]interface{}{"data": created})
}

func (h *AddressHandlers) GetAddressByID(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	vars := mux.Vars(r)
	addressID := vars["id"]

	if !uuidRegex.MatchString(addressID) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_ADDRESS_ID", "Invalid address ID format")
		return
	}

	addr, err := h.addressService.GetAddressByID(r.Context(), addressID, userID)
	if err != nil {
		switch err {
		case service.ErrAddressNotFound:
			h.respondWithError(w, http.StatusNotFound, "ADDRESS_NOT_FOUND", "Address not found")
		case service.ErrForbidden:
			h.respondWithError(w, http.StatusForbidden, "FORBIDDEN", "You do not own this address")
		default:
			h.logger.WithError(err).Error("Failed to get address")
			h.respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve address")
		}
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"data": addr})
}

func (h *AddressHandlers) GetMyAddresses(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)

	addresses, err := h.addressService.GetMyAddresses(r.Context(), userID)
	if err != nil {
		h.logger.WithError(err).Error("Failed to get addresses")
		h.respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve addresses")
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"data": addresses,
		"pagination": map[string]interface{}{
			"count":      len(addresses),
			"next_token": nil,
		},
	})
}

func (h *AddressHandlers) UpdateReceiverDetails(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	vars := mux.Vars(r)
	addressID := vars["id"]

	if !uuidRegex.MatchString(addressID) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_ADDRESS_ID", "Invalid address ID format")
		return
	}

	var req UpdateReceiverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	if req.ReceiverName == nil && req.ReceiverPhone == nil {
		h.respondWithError(w, http.StatusBadRequest, "EMPTY_UPDATE", "At least one field must be provided")
		return
	}

	updates := make(map[string]string)

	if req.ReceiverName != nil {
		if *req.ReceiverName == "" {
			h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "receiver_name cannot be empty")
			return
		}
		if len(*req.ReceiverName) > 128 {
			h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "receiver_name must be at most 128 characters")
			return
		}
		updates["receiver_name"] = *req.ReceiverName
	}

	if req.ReceiverPhone != nil {
		if !phoneRegex.MatchString(*req.ReceiverPhone) {
			h.respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid receiver phone number format")
			return
		}
		updates["receiver_phone"] = *req.ReceiverPhone
	}

	updated, err := h.addressService.UpdateReceiverDetails(r.Context(), addressID, userID, updates)
	if err != nil {
		switch err {
		case service.ErrAddressNotFound:
			h.respondWithError(w, http.StatusNotFound, "ADDRESS_NOT_FOUND", "Address not found")
		case service.ErrForbidden:
			h.respondWithError(w, http.StatusForbidden, "FORBIDDEN", "You do not own this address")
		default:
			h.logger.WithError(err).Error("Failed to update address")
			h.respondWithError(w, http.StatusInternalServerError, "ADDRESS_UPDATE_FAILED", "Failed to update address")
		}
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"data": updated})
}

func (h *AddressHandlers) RemoveAddress(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	vars := mux.Vars(r)
	addressID := vars["id"]

	if !uuidRegex.MatchString(addressID) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_ADDRESS_ID", "Invalid address ID format")
		return
	}

	err := h.addressService.RemoveAddress(r.Context(), addressID, userID)
	if err != nil {
		switch err {
		case service.ErrAddressNotFound:
			h.respondWithError(w, http.StatusNotFound, "ADDRESS_NOT_FOUND", "Address not found")
		case service.ErrForbidden:
			h.respondWithError(w, http.StatusForbidden, "FORBIDDEN", "You do not own this address")
		default:
			h.logger.WithError(err).Error("Failed to remove address")
			h.respondWithError(w, http.StatusInternalServerError, "ADDRESS_DELETE_FAILED", "Failed to remove address")
		}
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"message": "Address removed successfully"})
}

func (h *AddressHandlers) GetSuggestedAddresses(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)

	latStr := r.URL.Query().Get("latitude")
	lngStr := r.URL.Query().Get("longitude")

	if latStr == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "latitude is required")
		return
	}
	if lngStr == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "longitude is required")
		return
	}

	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil || lat < -90 || lat > 90 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Latitude must be between -90 and 90")
		return
	}

	lng, err := strconv.ParseFloat(lngStr, 64)
	if err != nil || lng < -180 || lng > 180 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Longitude must be between -180 and 180")
		return
	}

	suggested, err := h.addressService.GetSuggestedAddresses(r.Context(), userID, lat, lng)
	if err != nil {
		h.logger.WithError(err).Error("Failed to get suggested addresses")
		h.respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve addresses")
		return
	}

	if suggested == nil {
		suggested = []models.SuggestedAddress{}
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"data":  suggested,
		"count": len(suggested),
	})
}

func (h *AddressHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *AddressHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
