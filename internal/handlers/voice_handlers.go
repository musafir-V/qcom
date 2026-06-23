package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
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

// tripGetter abstracts TripRepository.GetByOrderID so the handler is testable
// without a real DynamoDB connection.
type tripGetter interface {
	GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error)
}

// callCounter abstracts CallRecordRepository.CountByTripDirection so the
// handler is testable without a real DynamoDB connection.
type callCounter interface {
	CountByTripDirection(ctx context.Context, tripID, direction string) (int, error)
}

// callRecorder abstracts CallRecordRepository.Upsert so the handler is
// testable without a real DynamoDB connection.
type callRecorder interface {
	Upsert(ctx context.Context, rec *models.CallRecord) error
}

// VoiceHandlers handles VoIP-related HTTP endpoints.
type VoiceHandlers struct {
	tokenSvc   *service.VoiceTokenService
	ensurer    userEnsurer
	trips      tripGetter
	callCounts callCounter
	recorder   callRecorder
	secret     string
	logger     *logrus.Logger
}

// NewVoiceHandlers constructs a VoiceHandlers.
func NewVoiceHandlers(
	tokenSvc *service.VoiceTokenService,
	ensurer userEnsurer,
	trips tripGetter,
	callCounts callCounter,
	recorder callRecorder,
	signatureSecret string,
	logger *logrus.Logger,
) *VoiceHandlers {
	return &VoiceHandlers{
		tokenSvc:   tokenSvc,
		ensurer:    ensurer,
		trips:      trips,
		callCounts: callCounts,
		recorder:   recorder,
		secret:     signatureSecret,
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
		OrderID   string `json:"order_id"`
		Direction string `json:"direction"`
	} `json:"custom_data"`
}

// AnswerWebhook handles POST /webhooks/voice/answer.
// Vonage calls this when an SDK user places an app-to-app call.
// It authorizes by Trip and returns an NCCO. Always responds HTTP 200
// (Vonage requires a valid NCCO even on reject).
func (h *VoiceHandlers) AnswerWebhook(w http.ResponseWriter, r *http.Request) {
	var req answerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CustomData.OrderID == "" {
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("bad_request"))
		return
	}

	trip, err := h.trips.GetByOrderID(r.Context(), req.CustomData.OrderID)
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

	count, err := h.callCounts.CountByTripDirection(r.Context(), trip.TripID, direction)
	if err != nil {
		h.logger.WithError(err).Warn("voice: CountByTripDirection failed, cap check skipped")
	}
	if count >= models.CallCapPerDirection {
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("cap_exceeded"))
		return
	}

	h.respondWithJSON(w, http.StatusOK, service.ConnectAppNCCO(toUser))
}

// eventReq is the inbound payload Vonage POSTs to the event webhook.
type eventReq struct {
	UUID     string `json:"uuid"`
	From     string `json:"from"`
	Status   string `json:"status"`
	Duration string `json:"duration"`
	CustomData struct {
		OrderID   string `json:"order_id"`
		Direction string `json:"direction"`
	} `json:"custom_data"`
}

// EventWebhook handles POST /webhooks/voice/event.
// Vonage calls this as a call progresses through its lifecycle.
// It verifies the Vonage HS256 signature, then upserts a CallRecord
// (idempotent: keyed on CALL!<uuid>).
func (h *VoiceHandlers) EventWebhook(w http.ResponseWriter, r *http.Request) {
	if !service.VerifyVonageSignature(r.Header.Get("Authorization"), h.secret) {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid signature")
		return
	}

	var req eventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}

	// FIX 3: guard missing identifiers before Upsert to avoid Vonage retry storms.
	if req.UUID == "" || req.CustomData.OrderID == "" {
		h.logger.Warn("voice: EventWebhook: missing uuid or order_id, skipping upsert")
		h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	// Resolve the trip by order_id to get the canonical TripID (DDB key) and
	// FIX 1: derive direction server-side so the rate cap cannot be evaded by
	// rotating custom_data.direction on the client.
	trip, tripErr := h.trips.GetByOrderID(r.Context(), req.CustomData.OrderID)
	if tripErr != nil || trip == nil {
		h.logger.WithError(tripErr).Warn("voice: EventWebhook: trip not found by order_id, skipping upsert")
		h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	direction := req.CustomData.Direction // fallback for unresolvable events
	if _, dir, ok := service.ResolveCounterpart(trip, req.From); ok {
		direction = dir
	}

	dur, err := strconv.Atoi(req.Duration)
	if err != nil {
		dur = 0
	}

	rec := models.CallRecord{
		TripID:      trip.TripID,
		CallID:      req.UUID,
		Direction:   direction,
		Status:      req.Status,
		Answered:    req.Status == "answered" || req.Status == "completed",
		DurationSec: dur,
	}

	if err := h.recorder.Upsert(r.Context(), &rec); err != nil {
		h.logger.WithError(err).Error("voice: EventWebhook: Upsert failed")
		h.respondWithError(w, http.StatusInternalServerError, "UPSERT_FAILED", "failed to persist call record")
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
