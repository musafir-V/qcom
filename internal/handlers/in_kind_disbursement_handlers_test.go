package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// --- stubs ---

type stubInKindDisbRepo struct {
	created []*models.InKindDisbursement
	listed  []*models.InKindDisbursement
}

func (s *stubInKindDisbRepo) Create(_ context.Context, d *models.InKindDisbursement) error {
	d.DisbursementID = "IK0001234567"
	d.DisbursedAt = "2026-07-02T10:00:00Z"
	s.created = append(s.created, d)
	return nil
}
func (s *stubInKindDisbRepo) ListByDE(_ context.Context, _ string) ([]*models.InKindDisbursement, error) {
	return s.listed, nil
}

type stubInKindEarningQuerier struct {
	entries []*models.EarningsLedger
}

func (s *stubInKindEarningQuerier) QueryByDE(_ context.Context, _ string, _ string, _ int32, _ map[string]types.AttributeValue) ([]*models.EarningsLedger, map[string]types.AttributeValue, error) {
	return s.entries, nil, nil
}

type stubInKindDEReader struct {
	de *models.DeliveryExecutive
}

func (s *stubInKindDEReader) GetByPhone(_ context.Context, _ string) (*models.DeliveryExecutive, error) {
	return s.de, nil
}

type stubInKindNotifier struct{}

func (s *stubInKindNotifier) Send(_ context.Context, _ models.NotificationSendRequest) models.NotificationSendResult {
	return models.NotificationSendResult{}
}

// --- tests ---

func newInKindRouter(h *InKindDisbursementHandlers) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/admin/drivers/{phone}/inkind-disbursements", h.RecordInKindDisbursement).Methods("POST")
	r.HandleFunc("/admin/drivers/{phone}/inkind-disbursements", h.ListInKindDisbursements).Methods("GET")
	return r
}

func TestRecordInKindDisbursement_Success(t *testing.T) {
	disbRepo := &stubInKindDisbRepo{}
	earningRepo := &stubInKindEarningQuerier{
		entries: []*models.EarningsLedger{
			{EarningID: "e1", Type: models.EarningTypeMealieBag, AmountZMW: 0},
			{EarningID: "e2", Type: models.EarningTypeMealieBag, AmountZMW: 0},
		},
	}
	h := &InKindDisbursementHandlers{
		inKindDisbRepo:     disbRepo,
		earningsLedgerRepo: earningRepo,
		deRepo:             &stubInKindDEReader{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971234567"}},
		notifier:           &stubInKindNotifier{},
		logger:             logrus.New(),
	}
	router := newInKindRouter(h)

	body, _ := json.Marshal(map[string]interface{}{"sku": "mealie_bag", "quantity": 1, "notes": "handover"})
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/+260971234567/inkind-disbursements", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), "entity_id", "admin-user-1"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(disbRepo.created) != 1 {
		t.Fatalf("expected 1 record created, got %d", len(disbRepo.created))
	}
	if disbRepo.created[0].DisbursedBy != "admin-user-1" {
		t.Errorf("disbursed_by = %q, want %q", disbRepo.created[0].DisbursedBy, "admin-user-1")
	}
	if disbRepo.created[0].SKU != models.InKindSKUMealieBag {
		t.Errorf("sku = %q", disbRepo.created[0].SKU)
	}
}

func TestRecordInKindDisbursement_InvalidSKU(t *testing.T) {
	h := &InKindDisbursementHandlers{
		inKindDisbRepo:     &stubInKindDisbRepo{},
		earningsLedgerRepo: &stubInKindEarningQuerier{},
		deRepo:             &stubInKindDEReader{de: &models.DeliveryExecutive{DEID: "de-1"}},
		notifier:           &stubInKindNotifier{},
		logger:             logrus.New(),
	}
	router := newInKindRouter(h)

	body, _ := json.Marshal(map[string]interface{}{"sku": "weekly_gift", "quantity": 1})
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/+260971234567/inkind-disbursements", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), "entity_id", "admin-1"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRecordInKindDisbursement_OverDisbursement(t *testing.T) {
	// Driver earned 1 bag, already disbursed 1 — outstanding = 0
	disbRepo := &stubInKindDisbRepo{
		listed: []*models.InKindDisbursement{
			{SKU: models.InKindSKUMealieBag, Quantity: 1},
		},
	}
	earningRepo := &stubInKindEarningQuerier{
		entries: []*models.EarningsLedger{
			{EarningID: "e1", Type: models.EarningTypeMealieBag, AmountZMW: 0},
		},
	}
	h := &InKindDisbursementHandlers{
		inKindDisbRepo:     disbRepo,
		earningsLedgerRepo: earningRepo,
		deRepo:             &stubInKindDEReader{de: &models.DeliveryExecutive{DEID: "de-1"}},
		notifier:           &stubInKindNotifier{},
		logger:             logrus.New(),
	}
	router := newInKindRouter(h)

	body, _ := json.Marshal(map[string]interface{}{"sku": "mealie_bag", "quantity": 1})
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/+260971234567/inkind-disbursements", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), "entity_id", "admin-1"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 OVER_DISBURSEMENT, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecordInKindDisbursement_ZeroQuantity(t *testing.T) {
	h := &InKindDisbursementHandlers{
		inKindDisbRepo:     &stubInKindDisbRepo{},
		earningsLedgerRepo: &stubInKindEarningQuerier{},
		deRepo:             &stubInKindDEReader{de: &models.DeliveryExecutive{DEID: "de-1"}},
		notifier:           &stubInKindNotifier{},
		logger:             logrus.New(),
	}
	router := newInKindRouter(h)

	body, _ := json.Marshal(map[string]interface{}{"sku": "mealie_bag", "quantity": 0})
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/+260971234567/inkind-disbursements", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), "entity_id", "admin-1"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestListInKindDisbursements_Success(t *testing.T) {
	disbRepo := &stubInKindDisbRepo{
		listed: []*models.InKindDisbursement{
			{DisbursementID: "IK001", SKU: models.InKindSKUMealieBag, Quantity: 2, DisbursedBy: "admin-1", DisbursedAt: "2026-07-01T10:00:00Z"},
		},
	}
	h := &InKindDisbursementHandlers{
		inKindDisbRepo:     disbRepo,
		earningsLedgerRepo: &stubInKindEarningQuerier{},
		deRepo:             &stubInKindDEReader{de: &models.DeliveryExecutive{DEID: "de-1"}},
		notifier:           &stubInKindNotifier{},
		logger:             logrus.New(),
	}
	router := newInKindRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/admin/drivers/+260971234567/inkind-disbursements", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Disbursements []struct {
			DisbursementID string `json:"disbursement_id"`
			SKU            string `json:"sku"`
			Quantity       int    `json:"quantity"`
		} `json:"disbursements"`
	}
	json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Disbursements) != 1 {
		t.Fatalf("expected 1 disbursement, got %d", len(body.Disbursements))
	}
	if body.Disbursements[0].SKU != "mealie_bag" || body.Disbursements[0].Quantity != 2 {
		t.Errorf("unexpected disbursement: %+v", body.Disbursements[0])
	}
}
