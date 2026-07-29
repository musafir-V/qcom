package handlers

import (
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
	Tag              string  `json:"tag"`
}

type UpdateReceiverRequest struct {
	ReceiverName  *string `json:"receiver_name"`
	ReceiverPhone *string `json:"receiver_phone"`
}

var phoneRegex = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)

// addressIDRegex accepts the current prefixed entity-ID format (a two-letter
// prefix followed by 10 digits, e.g. "AD1609067713") as well as the legacy
// UUID format used for addresses created before the ID scheme changed.
var addressIDRegex = regexp.MustCompile(`^(?:[A-Z]{2}\d{10}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
var validTags = map[string]bool{"home": true, "work": true, "other": true}

func (h *AddressHandlers) CreateAddress(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireEntityID(w, r, "Entity ID not found in token")
	if !ok {
		return
	}

	var req CreateAddressRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if req.ReceiverName == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "receiver_name is required")
		return
	}
	if len(req.ReceiverName) > 128 {
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "receiver_name must be at most 128 characters")
		return
	}
	if req.ReceiverPhone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "receiver_phone is required")
		return
	}
	if !phoneRegex.MatchString(req.ReceiverPhone) {
		respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid receiver phone number format")
		return
	}
	if req.BuildingAndFloor == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "building_and_floor is required")
		return
	}
	if len(req.BuildingAndFloor) > 256 {
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "building_and_floor must be at most 256 characters")
		return
	}
	if req.AddressLine1 == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "address_line_1 is required")
		return
	}
	if len(req.AddressLine1) > 256 {
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "address_line_1 must be at most 256 characters")
		return
	}
	if len(req.AddressLine2) > 256 {
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "address_line_2 must be at most 256 characters")
		return
	}
	if req.Latitude < -90 || req.Latitude > 90 {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Latitude must be between -90 and 90")
		return
	}
	if req.Longitude < -180 || req.Longitude > 180 {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Longitude must be between -180 and 180")
		return
	}
	if req.Tag != "" && !validTags[req.Tag] {
		respondWithError(w, http.StatusBadRequest, "INVALID_TAG", "tag must be one of: home, work, other")
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
		Tag:              req.Tag,
	}

	created, err := h.addressService.CreateAddress(r.Context(), userID, addr)
	if err != nil {
		h.logger.WithError(err).Error("Failed to create address")
		respondWithError(w, http.StatusInternalServerError, "ADDRESS_CREATION_FAILED", "Failed to create address")
		return
	}

	respondWithJSON(w, http.StatusCreated, map[string]interface{}{"data": created})
}

func (h *AddressHandlers) GetAddressByID(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireEntityID(w, r, "Entity ID not found in token")
	if !ok {
		return
	}
	vars := mux.Vars(r)
	addressID := vars["id"]

	if !addressIDRegex.MatchString(addressID) {
		respondWithError(w, http.StatusBadRequest, "INVALID_ADDRESS_ID", "Invalid address ID format")
		return
	}

	addr, err := h.addressService.GetAddressByID(r.Context(), addressID, userID)
	if err != nil {
		switch err {
		case service.ErrAddressNotFound:
			respondWithError(w, http.StatusNotFound, "ADDRESS_NOT_FOUND", "Address not found")
		case service.ErrForbidden:
			respondWithError(w, http.StatusForbidden, "FORBIDDEN", "You do not own this address")
		default:
			h.logger.WithError(err).Error("Failed to get address")
			respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve address")
		}
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{"data": addr})
}

func (h *AddressHandlers) GetMyAddresses(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireEntityID(w, r, "Entity ID not found in token")
	if !ok {
		return
	}

	addresses, err := h.addressService.GetMyAddresses(r.Context(), userID)
	if err != nil {
		h.logger.WithError(err).Error("Failed to get addresses")
		respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve addresses")
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"data": addresses,
		"pagination": map[string]interface{}{
			"count":      len(addresses),
			"next_token": nil,
		},
	})
}

func (h *AddressHandlers) UpdateReceiverDetails(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireEntityID(w, r, "Entity ID not found in token")
	if !ok {
		return
	}
	vars := mux.Vars(r)
	addressID := vars["id"]

	if !addressIDRegex.MatchString(addressID) {
		respondWithError(w, http.StatusBadRequest, "INVALID_ADDRESS_ID", "Invalid address ID format")
		return
	}

	var req UpdateReceiverRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if req.ReceiverName == nil && req.ReceiverPhone == nil {
		respondWithError(w, http.StatusBadRequest, "EMPTY_UPDATE", "At least one field must be provided")
		return
	}

	updates := make(map[string]string)

	if req.ReceiverName != nil {
		if *req.ReceiverName == "" {
			respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "receiver_name cannot be empty")
			return
		}
		if len(*req.ReceiverName) > 128 {
			respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "receiver_name must be at most 128 characters")
			return
		}
		updates["receiver_name"] = *req.ReceiverName
	}

	if req.ReceiverPhone != nil {
		if !phoneRegex.MatchString(*req.ReceiverPhone) {
			respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid receiver phone number format")
			return
		}
		updates["receiver_phone"] = *req.ReceiverPhone
	}

	updated, err := h.addressService.UpdateReceiverDetails(r.Context(), addressID, userID, updates)
	if err != nil {
		switch err {
		case service.ErrAddressNotFound:
			respondWithError(w, http.StatusNotFound, "ADDRESS_NOT_FOUND", "Address not found")
		case service.ErrForbidden:
			respondWithError(w, http.StatusForbidden, "FORBIDDEN", "You do not own this address")
		default:
			h.logger.WithError(err).Error("Failed to update address")
			respondWithError(w, http.StatusInternalServerError, "ADDRESS_UPDATE_FAILED", "Failed to update address")
		}
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{"data": updated})
}

func (h *AddressHandlers) RemoveAddress(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireEntityID(w, r, "Entity ID not found in token")
	if !ok {
		return
	}
	vars := mux.Vars(r)
	addressID := vars["id"]

	if !addressIDRegex.MatchString(addressID) {
		respondWithError(w, http.StatusBadRequest, "INVALID_ADDRESS_ID", "Invalid address ID format")
		return
	}

	err := h.addressService.RemoveAddress(r.Context(), addressID, userID)
	if err != nil {
		switch err {
		case service.ErrAddressNotFound:
			respondWithError(w, http.StatusNotFound, "ADDRESS_NOT_FOUND", "Address not found")
		case service.ErrForbidden:
			respondWithError(w, http.StatusForbidden, "FORBIDDEN", "You do not own this address")
		default:
			h.logger.WithError(err).Error("Failed to remove address")
			respondWithError(w, http.StatusInternalServerError, "ADDRESS_DELETE_FAILED", "Failed to remove address")
		}
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"message": "Address removed successfully"})
}

func (h *AddressHandlers) GetSuggestedAddresses(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireEntityID(w, r, "Entity ID not found in token")
	if !ok {
		return
	}

	latStr := r.URL.Query().Get("latitude")
	lngStr := r.URL.Query().Get("longitude")

	if latStr == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "latitude is required")
		return
	}
	if lngStr == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "longitude is required")
		return
	}

	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil || lat < -90 || lat > 90 {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Latitude must be between -90 and 90")
		return
	}

	lng, err := strconv.ParseFloat(lngStr, 64)
	if err != nil || lng < -180 || lng > 180 {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Longitude must be between -180 and 180")
		return
	}

	suggested, err := h.addressService.GetSuggestedAddresses(r.Context(), userID, lat, lng)
	if err != nil {
		h.logger.WithError(err).Error("Failed to get suggested addresses")
		respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve addresses")
		return
	}

	if suggested == nil {
		suggested = []models.SuggestedAddress{}
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"data":  suggested,
		"count": len(suggested),
	})
}
