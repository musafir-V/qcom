package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type adminRuleRepository interface {
	ListAll(ctx context.Context) ([]*models.Rule, error)
	Put(ctx context.Context, rule *models.Rule) error
}

type AdminRulesHandlers struct {
	repo   adminRuleRepository
	logger *logrus.Logger
}

func NewAdminRulesHandlers(repo adminRuleRepository, logger *logrus.Logger) *AdminRulesHandlers {
	return &AdminRulesHandlers{repo: repo, logger: logger}
}

func AdminKeyMiddleware(expectedKey string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.TrimSpace(expectedKey) == "" || r.Header.Get("X-Admin-Key") != expectedKey {
				adminRulesRespondWithJSON(w, http.StatusUnauthorized, ErrorResponse{
					Error: ErrorDetail{Code: "UNAUTHORIZED", Message: "Unauthorized"},
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (h *AdminRulesHandlers) ListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.repo.ListAll(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("admin rules: list failed")
		h.respondWithError(w, http.StatusInternalServerError, "RULES_LIST_FAILED", "Failed to list rules")
		return
	}
	latest := latestRulesByID(rules)
	sort.Slice(latest, func(i, j int) bool { return latest[i].ID < latest[j].ID })
	h.respondWithJSON(w, http.StatusOK, latest)
}

// ListRuleVersions returns every stored version of a rule id, newest first.
func (h *AdminRulesHandlers) ListRuleVersions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "id is required")
		return
	}
	rules, err := h.repo.ListAll(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("admin rules: list versions failed")
		h.respondWithError(w, http.StatusInternalServerError, "RULES_LIST_FAILED", "Failed to list rule versions")
		return
	}
	versions := make([]models.Rule, 0)
	for _, rule := range rules {
		if rule != nil && rule.ID == id {
			cp := *rule
			cp.Spec = cloneRaw(rule.Spec)
			versions = append(versions, cp)
		}
	}
	if len(versions) == 0 {
		h.respondWithError(w, http.StatusNotFound, "RULE_NOT_FOUND", "rule not found")
		return
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	h.respondWithJSON(w, http.StatusOK, versions)
}

func (h *AdminRulesHandlers) CreateRule(w http.ResponseWriter, r *http.Request) {
	var req models.Rule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Name = strings.TrimSpace(req.Name)
	if req.ID == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "id is required")
		return
	}
	if err := validateRuleRequest(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_RULE", err.Error())
		return
	}

	rules, err := h.repo.ListAll(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("admin rules: list before create failed")
		h.respondWithError(w, http.StatusInternalServerError, "RULES_WRITE_FAILED", "Failed to create rule")
		return
	}
	if latestByIDMap(rules)[req.ID] != nil {
		h.respondWithError(w, http.StatusConflict, "RULE_EXISTS", "rule id already exists")
		return
	}

	req.Version = 1
	req.Spec = cloneRaw(req.Spec)
	if err := h.repo.Put(r.Context(), &req); err != nil {
		h.logger.WithError(err).Error("admin rules: create failed")
		h.respondWithError(w, http.StatusInternalServerError, "RULES_WRITE_FAILED", "Failed to create rule")
		return
	}
	h.respondWithJSON(w, http.StatusCreated, req)
}

func (h *AdminRulesHandlers) UpdateRule(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "id is required")
		return
	}
	var req models.Rule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.ID) != "" && strings.TrimSpace(req.ID) != id {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_RULE", "id in path and body must match")
		return
	}
	req.ID = id
	req.Name = strings.TrimSpace(req.Name)
	if err := validateRuleRequest(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_RULE", err.Error())
		return
	}

	rules, err := h.repo.ListAll(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("admin rules: list before update failed")
		h.respondWithError(w, http.StatusInternalServerError, "RULES_WRITE_FAILED", "Failed to update rule")
		return
	}
	latest := latestByIDMap(rules)[id]
	if latest == nil {
		h.respondWithError(w, http.StatusNotFound, "RULE_NOT_FOUND", "rule not found")
		return
	}
	if latest.Family != req.Family {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_RULE", "family cannot change across versions")
		return
	}
	req.Version = latest.Version + 1
	req.Spec = cloneRaw(req.Spec)
	if err := h.repo.Put(r.Context(), &req); err != nil {
		h.logger.WithError(err).Error("admin rules: update failed")
		h.respondWithError(w, http.StatusInternalServerError, "RULES_WRITE_FAILED", "Failed to update rule")
		return
	}
	h.respondWithJSON(w, http.StatusCreated, req)
}

func (h *AdminRulesHandlers) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "id is required")
		return
	}
	rules, err := h.repo.ListAll(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("admin rules: list before delete failed")
		h.respondWithError(w, http.StatusInternalServerError, "RULES_WRITE_FAILED", "Failed to delete rule")
		return
	}
	latest := latestByIDMap(rules)[id]
	if latest == nil {
		h.respondWithError(w, http.StatusNotFound, "RULE_NOT_FOUND", "rule not found")
		return
	}

	next := *latest
	next.Version = latest.Version + 1
	next.Enabled = false
	next.Spec = cloneRaw(latest.Spec)

	if err := h.repo.Put(r.Context(), &next); err != nil {
		h.logger.WithError(err).Error("admin rules: delete failed")
		h.respondWithError(w, http.StatusInternalServerError, "RULES_WRITE_FAILED", "Failed to delete rule")
		return
	}
	h.respondWithJSON(w, http.StatusCreated, next)
}

func validateRuleRequest(rule *models.Rule) error {
	if strings.TrimSpace(rule.ID) == "" {
		return errors.New("id is required")
	}
	if rule.Family == "" {
		return errors.New("family is required")
	}
	if err := validateEffectiveWindow(rule.EffectiveFrom, rule.EffectiveTo); err != nil {
		return err
	}

	switch rule.Family {
	case models.FamilyRateModifier:
		return validateRateModifierSpec(rule.Spec)
	case models.FamilyAccumulator:
		return validateAccumulatorSpec(rule.Spec)
	case models.FamilyRanking:
		return validateRankingSpec(rule.Spec)
	default:
		return errors.New("unknown family")
	}
}

func validateEffectiveWindow(fromRaw, toRaw *string) error {
	if fromRaw == nil || toRaw == nil || *fromRaw == "" || *toRaw == "" {
		return nil
	}
	from, err := time.Parse(time.RFC3339, *fromRaw)
	if err != nil {
		return errors.New("effective_from must be RFC3339")
	}
	to, err := time.Parse(time.RFC3339, *toRaw)
	if err != nil {
		return errors.New("effective_to must be RFC3339")
	}
	if to.Before(from) {
		return errors.New("effective_to must be on or after effective_from")
	}
	return nil
}

func validateRateModifierSpec(raw json.RawMessage) error {
	var spec models.RateModifierSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return errors.New("invalid rate_modifier spec")
	}
	if spec.Multiplier < 1 {
		return errors.New("multiplier must be >= 1")
	}
	if spec.FlatZMW < 0 {
		return errors.New("flat_zmw must be >= 0")
	}
	if spec.StartTime != "" {
		if _, err := time.Parse("15:04", spec.StartTime); err != nil {
			return errors.New("start_time must be HH:MM")
		}
	}
	if spec.EndTime != "" {
		if _, err := time.Parse("15:04", spec.EndTime); err != nil {
			return errors.New("end_time must be HH:MM")
		}
	}
	return nil
}

func validateAccumulatorSpec(raw json.RawMessage) error {
	var spec models.AccumulatorSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return errors.New("invalid accumulator spec")
	}
	if spec.Threshold <= 0 {
		return errors.New("threshold must be > 0")
	}
	if spec.MinOnTimeRate < 0 || spec.MinOnTimeRate > 1 {
		return errors.New("min_on_time_rate must be between 0 and 1")
	}
	if spec.Window != "daily" && spec.Window != "weekly" {
		return errors.New("window must be one of: daily, weekly")
	}
	return nil
}

func validateRankingSpec(raw json.RawMessage) error {
	var spec models.RankingSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return errors.New("invalid ranking spec")
	}
	if spec.TopN <= 0 {
		return errors.New("top_n must be > 0")
	}
	if spec.WeightRate < 0 || spec.WeightVolume < 0 {
		return errors.New("weights must be >= 0")
	}
	if spec.Window != "weekly" {
		return errors.New("window must be one of: weekly")
	}
	return nil
}

func latestRulesByID(rules []*models.Rule) []models.Rule {
	latestMap := latestByIDMap(rules)
	out := make([]models.Rule, 0, len(latestMap))
	for _, rule := range latestMap {
		if rule == nil {
			continue
		}
		cp := *rule
		cp.Spec = cloneRaw(rule.Spec)
		out = append(out, cp)
	}
	return out
}

func latestByIDMap(rules []*models.Rule) map[string]*models.Rule {
	latest := make(map[string]*models.Rule)
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		current := latest[rule.ID]
		if current == nil || rule.Version > current.Version {
			latest[rule.ID] = rule
		}
	}
	return latest
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append([]byte(nil), raw...)
}

func (h *AdminRulesHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	adminRulesRespondWithJSON(w, status, payload)
}

func (h *AdminRulesHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	adminRulesRespondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}

func adminRulesRespondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
