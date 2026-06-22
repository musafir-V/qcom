package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type stubAdminRulesRepo struct {
	rules []*models.Rule
	puts  []*models.Rule
}

func (s *stubAdminRulesRepo) ListAll(_ context.Context) ([]*models.Rule, error) {
	out := make([]*models.Rule, 0, len(s.rules))
	out = append(out, s.rules...)
	out = append(out, s.puts...)
	return out, nil
}

func (s *stubAdminRulesRepo) Put(_ context.Context, rule *models.Rule) error {
	cp := *rule
	cp.Spec = append([]byte(nil), rule.Spec...)
	s.puts = append(s.puts, &cp)
	return nil
}

func setupAdminRulesRouter(t *testing.T, repo *stubAdminRulesRepo, adminKey string) *mux.Router {
	t.Helper()
	h := NewAdminRulesHandlers(repo, logrus.New())
	router := mux.NewRouter()
	admin := router.PathPrefix("/admin").Subrouter()
	rules := admin.PathPrefix("/rules").Subrouter()
	rules.Use(AdminKeyMiddleware(adminKey))
	rules.HandleFunc("", h.ListRules).Methods(http.MethodGet)
	rules.HandleFunc("", h.CreateRule).Methods(http.MethodPost)
	rules.HandleFunc("/{id}", h.UpdateRule).Methods(http.MethodPut)
	rules.HandleFunc("/{id}", h.DeleteRule).Methods(http.MethodDelete)
	return router
}

func newAdminRequest(t *testing.T, method, path, adminKey string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if adminKey != "" {
		req.Header.Set("X-Admin-Key", adminKey)
	}
	return req
}

func mustMarshalSpec(t *testing.T, spec any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return raw
}

func validRateRulePayload(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"name":     "Night Peak",
		"family":   "rate_modifier",
		"enabled":  true,
		"priority": 100,
		"spec": map[string]any{
			"days_of_week": []int{1, 2, 3},
			"start_time":   "18:00",
			"end_time":     "22:00",
			"multiplier":   1.2,
			"flat_zmw":     0,
		},
	}
}

func TestAdminRulesMiddlewareUnauthorized(t *testing.T) {
	router := setupAdminRulesRouter(t, &stubAdminRulesRepo{}, "secret")
	req := newAdminRequest(t, http.MethodGet, "/admin/rules", "", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	reqWrong := newAdminRequest(t, http.MethodGet, "/admin/rules", "wrong", nil)
	recWrong := httptest.NewRecorder()
	router.ServeHTTP(recWrong, reqWrong)
	if recWrong.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for wrong key", recWrong.Code)
	}
}

func TestCreateRuleValidationRejectsBadCases(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "unknown family",
			body: map[string]any{
				"id":       "r-1",
				"name":     "Unknown",
				"family":   "unknown",
				"enabled":  true,
				"priority": 1,
				"spec":     map[string]any{},
			},
		},
		{
			name: "common effective_to before effective_from",
			body: map[string]any{
				"id":             "r-1",
				"name":           "Night Peak",
				"family":         "rate_modifier",
				"enabled":        true,
				"priority":       1,
				"effective_from": "2026-06-22T10:00:00Z",
				"effective_to":   "2026-06-21T10:00:00Z",
				"spec": map[string]any{
					"multiplier": 1.1,
				},
			},
		},
		{
			name: "rate multiplier less than one",
			body: map[string]any{
				"id":       "r-1",
				"name":     "Night Peak",
				"family":   "rate_modifier",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"multiplier": 0.99,
				},
			},
		},
		{
			name: "rate flat negative",
			body: map[string]any{
				"id":       "r-1",
				"name":     "Night Peak",
				"family":   "rate_modifier",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"multiplier": 1.2,
					"flat_zmw":   -1,
				},
			},
		},
		{
			name: "rate bad start time",
			body: map[string]any{
				"id":       "r-1",
				"name":     "Night Peak",
				"family":   "rate_modifier",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"multiplier": 1.2,
					"start_time": "26:00",
				},
			},
		},
		{
			name: "rate bad end time",
			body: map[string]any{
				"id":       "r-1",
				"name":     "Night Peak",
				"family":   "rate_modifier",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"multiplier": 1.2,
					"end_time":   "ab:cd",
				},
			},
		},
		{
			name: "accumulator threshold non-positive",
			body: map[string]any{
				"id":       "a-1",
				"name":     "Daily",
				"family":   "accumulator",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"window":    "daily",
					"threshold": 0,
				},
			},
		},
		{
			name: "accumulator min on-time rate out of range",
			body: map[string]any{
				"id":       "a-1",
				"name":     "Daily",
				"family":   "accumulator",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"window":           "daily",
					"threshold":        10,
					"min_on_time_rate": 1.5,
				},
			},
		},
		{
			name: "accumulator unknown window",
			body: map[string]any{
				"id":       "a-1",
				"name":     "Daily",
				"family":   "accumulator",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"window":    "monthly",
					"threshold": 10,
				},
			},
		},
		{
			name: "ranking top_n non-positive",
			body: map[string]any{
				"id":       "rk-1",
				"name":     "Weekly rank",
				"family":   "ranking",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"window": "weekly",
					"top_n":  0,
				},
			},
		},
		{
			name: "ranking negative weights",
			body: map[string]any{
				"id":       "rk-1",
				"name":     "Weekly rank",
				"family":   "ranking",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"window":        "weekly",
					"top_n":         3,
					"weight_rate":   -0.1,
					"weight_volume": 0.2,
				},
			},
		},
		{
			name: "ranking unknown window",
			body: map[string]any{
				"id":       "rk-1",
				"name":     "Weekly rank",
				"family":   "ranking",
				"enabled":  true,
				"priority": 1,
				"spec": map[string]any{
					"window": "daily",
					"top_n":  3,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubAdminRulesRepo{}
			router := setupAdminRulesRouter(t, repo, "secret")
			req := newAdminRequest(t, http.MethodPost, "/admin/rules", "secret", tc.body)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
			}
			if len(repo.puts) != 0 {
				t.Fatalf("puts = %d, want 0 on validation error", len(repo.puts))
			}
		})
	}
}

func TestUpdateRuleIncrementsVersion(t *testing.T) {
	repo := &stubAdminRulesRepo{
		rules: []*models.Rule{
			{
				ID:      "night_peak",
				Name:    "Night Peak",
				Family:  models.FamilyRateModifier,
				Enabled: true, Version: 1, Priority: 50,
				Spec: mustMarshalSpec(t, models.RateModifierSpec{
					Multiplier: 1.1,
				}),
			},
		},
	}
	router := setupAdminRulesRouter(t, repo, "secret")
	body := validRateRulePayload(t)
	body["id"] = "night_peak"

	req := newAdminRequest(t, http.MethodPut, "/admin/rules/night_peak", "secret", body)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 body=%s", rec.Code, rec.Body.String())
	}
	if len(repo.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(repo.puts))
	}
	if repo.puts[0].Version != 2 {
		t.Fatalf("version = %d, want 2", repo.puts[0].Version)
	}
	if repo.puts[0].ID != "night_peak" {
		t.Fatalf("id = %q, want night_peak", repo.puts[0].ID)
	}
}

func TestDeleteRuleWritesDisabledNextVersion(t *testing.T) {
	repo := &stubAdminRulesRepo{
		rules: []*models.Rule{
			{
				ID:      "night_peak",
				Name:    "Night Peak",
				Family:  models.FamilyRateModifier,
				Enabled: true, Version: 2, Priority: 80,
				Spec: mustMarshalSpec(t, models.RateModifierSpec{
					Multiplier: 1.2,
				}),
			},
		},
	}
	router := setupAdminRulesRouter(t, repo, "secret")
	req := newAdminRequest(t, http.MethodDelete, "/admin/rules/night_peak", "secret", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 body=%s", rec.Code, rec.Body.String())
	}
	if len(repo.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(repo.puts))
	}
	if repo.puts[0].Version != 3 {
		t.Fatalf("version = %d, want 3", repo.puts[0].Version)
	}
	if repo.puts[0].Enabled {
		t.Fatalf("enabled = true, want false")
	}
}

func TestListRulesReturnsLatestVersionPerID(t *testing.T) {
	repo := &stubAdminRulesRepo{
		rules: []*models.Rule{
			{ID: "night_peak", Family: models.FamilyRateModifier, Version: 1, Name: "old", Enabled: true},
			{ID: "night_peak", Family: models.FamilyRateModifier, Version: 2, Name: "new", Enabled: true},
			{ID: "b2_weekly_bonus", Family: models.FamilyAccumulator, Version: 1, Name: "bonus", Enabled: true},
		},
	}
	router := setupAdminRulesRouter(t, repo, "secret")
	req := newAdminRequest(t, http.MethodGet, "/admin/rules", "secret", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var got []models.Rule
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rules length = %d, want 2", len(got))
	}

	byID := map[string]models.Rule{}
	for _, rule := range got {
		byID[rule.ID] = rule
	}
	if byID["night_peak"].Version != 2 {
		t.Fatalf("night_peak version = %d, want 2", byID["night_peak"].Version)
	}
	if strings.TrimSpace(byID["b2_weekly_bonus"].ID) == "" {
		t.Fatalf("expected b2_weekly_bonus in response")
	}
}
