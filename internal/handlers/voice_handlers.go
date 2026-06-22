package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

// userEnsurer abstracts VoiceProvisionService.EnsureUser so the handler is
// testable without a real Vonage connection or DynamoDB.
type userEnsurer interface {
	EnsureUser(ctx context.Context, sub string) error
}

// tripGetter abstracts TripRepository.GetByID so the handler is testable
// without a real DynamoDB connection.
type tripGetter interface {
	GetByID(ctx context.Context, tripID string) (*models.Trip, error)
}

// callCounter abstracts CallRecordRepository.CountByTripDirection so the
// handler is testable without a real DynamoDB connection.
type callCounter interface {
	CountByTripDirection(ctx context.Context, tripID, direction string) (int, error)
}

// VoiceHandlers handles VoIP-related HTTP endpoints.
type VoiceHandlers struct {
	tokenSvc   *service.VoiceTokenService
	ensurer    userEnsurer
	trips      tripGetter
	callCounts callCounter
	logger     *logrus.Logger
}

// NewVoiceHandlers constructs a VoiceHandlers.
func NewVoiceHandlers(
	tokenSvc *service.VoiceTokenService,
	ensurer userEnsurer,
	trips tripGetter,
	callCounts callCounter,
	logger *logrus.Logger,
) *VoiceHandlers {
	return &VoiceHandlers{
		tokenSvc:   tokenSvc,
		ensurer:    ensurer,
		trips:      trips,
		callCounts: callCounts,
		logger:     logger,
	}
}

type voiceTokenResponse struct {
	Token string `json:"token"`
	User  string `json:"user"`
	TTL   int    `json:"ttl"`
}

// PostToken handles POST /api/v1/voice/token.
// It maps the caller's qcom identity (entity_id / entity_type from the JWT
// middleware context) to a Vonage sub, lazily provisions the Vonage user,
// signs a per-user RS256 token, and returns {"token","user","ttl":3600}.
func (h *VoiceHandlers) PostToken(w http.ResponseWriter, r *http.Request) {
	entityID, _ := r.Context().Value("entity_id").(string)
	entityType, _ := r.Context().Value("entity_type").(string)
	if entityID == "" {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}

	var sub string
	switch entityType {
	case "de":
		sub = service.RiderVonageUser(entityID)
	case "customer":
		sub = service.CustomerVonageUser(entityID)
	default:
		h.respondWithError(w, http.StatusForbidden, "FORBIDDEN", "unsupported entity type")
		return
	}

	if err := h.ensurer.EnsureUser(r.Context(), sub); err != nil {
		h.logger.WithError(err).Error("voice: EnsureUser failed")
		h.respondWithError(w, http.StatusServiceUnavailable, "PROVISION_FAILED", "failed to provision voice user")
		return
	}

	token, err := h.tokenSvc.GenerateUserToken(sub)
	if err != nil {
		h.logger.WithError(err).Error("voice: GenerateUserToken failed")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GEN_FAILED", "failed to generate voice token")
		return
	}

	h.respondWithJSON(w, http.StatusOK, voiceTokenResponse{Token: token, User: sub, TTL: 3600})
}

// answerReq is the inbound payload Vonage POSTs to the answer webhook.
type answerReq struct {
	From       string `json:"from"`
	CustomData struct {
		TripID    string `json:"trip_id"`
		Direction string `json:"direction"`
	} `json:"custom_data"`
}

// AnswerWebhook handles POST /webhooks/voice/answer.
// Vonage calls this when an SDK user places an app-to-app call.
// It authorizes by Trip and returns an NCCO. Always responds HTTP 200
// (Vonage requires a valid NCCO even on reject).
func (h *VoiceHandlers) AnswerWebhook(w http.ResponseWriter, r *http.Request) {
	var req answerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CustomData.TripID == "" {
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("bad_request"))
		return
	}

	trip, err := h.trips.GetByID(r.Context(), req.CustomData.TripID)
	if err != nil || trip == nil {
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("trip_not_found"))
		return
	}

	if ok, _ := service.CanCall(trip, time.Now()); !ok {
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("trip_not_callable"))
		return
	}

	// Resolve the counterpart from the authenticated caller (From field).
	// Use this resolved direction — do NOT trust client-supplied custom_data.direction.
	toUser, direction, ok := service.ResolveCounterpart(trip, req.From)
	if !ok {
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("unknown_caller"))
		return
	}

	count, _ := h.callCounts.CountByTripDirection(r.Context(), trip.TripID, direction)
	if count >= models.CallCapPerDirection {
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("cap_exceeded"))
		return
	}

	h.respondWithJSON(w, http.StatusOK, service.ConnectAppNCCO(toUser))
}

func (h *VoiceHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *VoiceHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
