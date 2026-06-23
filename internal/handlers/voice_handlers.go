package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
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

// AnswerWebhook handles POST /webhooks/voice/answer.
// Vonage calls this when an SDK user places an app-to-app call.
// It authorizes by Trip and returns an NCCO. Always responds HTTP 200
// (Vonage requires a valid NCCO even on reject).
func (h *VoiceHandlers) AnswerWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.WithError(err).Warn("voice: AnswerWebhook: read body failed")
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("bad_request"))
		return
	}

	// Log the exact Vonage payload first — before any validation — so we can see
	// what custom_data keys/values arrived (order_id vs trip_id, etc.).
	h.logger.WithFields(logrus.Fields{
		"webhook":      "answer",
		"content_type": r.Header.Get("Content-Type"),
		"raw_body":     string(body),
	}).Info("voice: AnswerWebhook: inbound payload")

	req, decodeErr := parseVoiceWebhookBody(body)
	customDataKeys, customDataRaw := voiceCustomDataDebug(body)

	if decodeErr != nil {
		h.logger.WithError(decodeErr).WithFields(logrus.Fields{
			"webhook":          "answer",
			"outcome":          "reject",
			"reason":           "decode_failed",
			"custom_data_keys": customDataKeys,
			"custom_data_raw":  customDataRaw,
		}).Warn("voice: AnswerWebhook: invalid JSON body")
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("bad_request"))
		return
	}

	if req.CustomData.OrderID == "" {
		h.logger.WithFields(logrus.Fields{
			"webhook":          "answer",
			"outcome":          "reject",
			"reason":           "missing_order_id",
			"from":             req.From,
			"client_direction": req.CustomData.Direction,
			"custom_data_keys": customDataKeys,
			"custom_data_raw":  customDataRaw,
		}).Warn("voice: AnswerWebhook: custom_data.order_id empty")
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("bad_request"))
		return
	}

	h.logger.WithFields(logrus.Fields{
		"webhook":          "answer",
		"from":             req.From,
		"order_id":         req.CustomData.OrderID,
		"client_direction": req.CustomData.Direction,
		"custom_data_keys": customDataKeys,
	}).Info("voice: AnswerWebhook: received")

	trip, err := h.trips.GetByOrderID(r.Context(), req.CustomData.OrderID)
	if err != nil || trip == nil {
		h.logger.WithError(err).WithFields(logrus.Fields{
			"webhook":  "answer",
			"outcome":  "reject",
			"reason":   "trip_not_found",
			"from":     req.From,
			"order_id": req.CustomData.OrderID,
		}).Warn("voice: AnswerWebhook: trip lookup failed")
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("trip_not_found"))
		return
	}

	if ok, reason := service.CanCall(trip, time.Now()); !ok {
		h.logger.WithFields(logrus.Fields{
			"webhook":  "answer",
			"outcome":  "reject",
			"reason":   "trip_not_callable",
			"detail":   reason,
			"from":     req.From,
			"order_id": req.CustomData.OrderID,
			"trip_id":  trip.TripID,
			"status":   trip.Status,
		}).Warn("voice: AnswerWebhook: trip not callable")
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("trip_not_callable"))
		return
	}

	// Resolve the counterpart from the authenticated caller (From field).
	// Use this resolved direction — do NOT trust client-supplied custom_data.direction.
	toUser, direction, ok := service.ResolveCounterpart(trip, req.From)
	if !ok {
		h.logger.WithFields(logrus.Fields{
			"webhook":  "answer",
			"outcome":  "reject",
			"reason":   "unknown_caller",
			"from":     req.From,
			"order_id": req.CustomData.OrderID,
			"trip_id":  trip.TripID,
			"de_id":    trip.DEID,
			"customer": trip.CustomerUserID,
		}).Warn("voice: AnswerWebhook: caller does not match trip")
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("unknown_caller"))
		return
	}

	count, err := h.callCounts.CountByTripDirection(r.Context(), trip.TripID, direction)
	if err != nil {
		h.logger.WithError(err).Warn("voice: CountByTripDirection failed, cap check skipped")
	}
	if count >= models.CallCapPerDirection {
		h.logger.WithFields(logrus.Fields{
			"webhook":   "answer",
			"outcome":   "reject",
			"reason":    "cap_exceeded",
			"from":      req.From,
			"order_id":  req.CustomData.OrderID,
			"trip_id":   trip.TripID,
			"direction": direction,
			"count":     count,
		}).Warn("voice: AnswerWebhook: per-direction call cap reached")
		h.respondWithJSON(w, http.StatusOK, service.RejectNCCO("cap_exceeded"))
		return
	}

	h.logger.WithFields(logrus.Fields{
		"webhook":   "answer",
		"outcome":   "connect",
		"from":      req.From,
		"to":        toUser,
		"order_id":  req.CustomData.OrderID,
		"trip_id":   trip.TripID,
		"direction": direction,
	}).Info("voice: AnswerWebhook: connecting call")
	h.respondWithJSON(w, http.StatusOK, service.ConnectAppNCCO(toUser))
}

// EventWebhook handles POST /webhooks/voice/event.
// Vonage calls this as a call progresses through its lifecycle.
// It verifies the Vonage HS256 signature, then upserts a CallRecord
// (idempotent: keyed on CALL!<uuid>).
func (h *VoiceHandlers) EventWebhook(w http.ResponseWriter, r *http.Request) {
	if !service.VerifyVonageSignature(r.Header.Get("Authorization"), h.secret) {
		h.logger.WithFields(logrus.Fields{
			"webhook":    "event",
			"outcome":    "reject",
			"reason":     "invalid_signature",
			"has_auth":   r.Header.Get("Authorization") != "",
		}).Warn("voice: EventWebhook: signature verification failed")
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid signature")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.WithError(err).Warn("voice: EventWebhook: read body failed")
		h.respondWithError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}

	req, err := parseVoiceWebhookBody(body)
	if err != nil {
		h.logger.WithError(err).WithField("body_preview", truncateForLog(string(body), 512)).
			Warn("voice: EventWebhook: decode failed")
		h.respondWithError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}

	customDataKeys, customDataRaw := voiceCustomDataDebug(body)

	// FIX 3: guard missing identifiers before Upsert to avoid Vonage retry storms.
	if req.UUID == "" || req.CustomData.OrderID == "" {
		reason := "missing_identifiers"
		switch {
		case req.UUID == "" && req.CustomData.OrderID == "":
			reason = "missing_uuid_and_order_id"
		case req.UUID == "":
			reason = "missing_uuid"
		case req.CustomData.OrderID == "":
			reason = "missing_order_id"
		}
		h.logger.WithFields(logrus.Fields{
			"webhook":          "event",
			"outcome":          "skip",
			"reason":           reason,
			"uuid":             req.UUID,
			"order_id":         req.CustomData.OrderID,
			"from":             req.From,
			"status":           req.Status,
			"client_direction": req.CustomData.Direction,
			"custom_data_keys": customDataKeys,
			"custom_data_raw":  customDataRaw,
			"body_preview":     truncateForLog(string(body), 512),
		}).Warn("voice: EventWebhook: skipping upsert")
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

	h.logger.WithFields(logrus.Fields{
		"webhook":   "event",
		"outcome":   "upserted",
		"uuid":      req.UUID,
		"order_id":  req.CustomData.OrderID,
		"trip_id":   trip.TripID,
		"from":      req.From,
		"status":    req.Status,
		"direction": direction,
		"duration":  dur,
	}).Info("voice: EventWebhook: call record saved")
	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// voiceCustomDataDebug extracts custom_data key names and a truncated raw JSON
// blob so we can see whether Vonage forwarded order_id vs legacy trip_id, etc.
func voiceCustomDataDebug(body []byte) (keys []string, raw string) {
	var partial struct {
		CustomData json.RawMessage `json:"custom_data"`
	}
	if err := json.Unmarshal(body, &partial); err != nil || len(partial.CustomData) == 0 {
		return nil, ""
	}
	raw = truncateForLog(string(partial.CustomData), 256)
	keys = customDataKeys(partial.CustomData)
	if len(keys) > 0 {
		sort.Strings(keys)
		return keys, raw
	}
	// Legacy object form for debug output when keys couldn't be parsed yet.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(partial.CustomData, &fields); err != nil {
		return nil, raw
	}
	keys = make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, raw
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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
