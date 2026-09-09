package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type fakeArchiveDEService struct {
	de  *models.DeliveryExecutive
	err error
}

func (f *fakeArchiveDEService) ListDriversByStore(context.Context, string, string, string, int32, bool) ([]*models.DeliveryExecutive, string, error) {
	return nil, "", nil
}
func (f *fakeArchiveDEService) GetTodayEarnings(context.Context, string) (float64, error) {
	return 12.5, nil
}
func (f *fakeArchiveDEService) Register(context.Context, service.RegisterDERequest) (*models.DeliveryExecutive, error) {
	return nil, nil
}
func (f *fakeArchiveDEService) ReassignStore(context.Context, string, string) error { return nil }
func (f *fakeArchiveDEService) ArchiveDriver(context.Context, string) (*models.DeliveryExecutive, error) {
	return f.de, f.err
}
func (f *fakeArchiveDEService) RestoreDriver(context.Context, string) (*models.DeliveryExecutive, error) {
	return f.de, f.err
}

func doDriverArchive(h *AdminDriverHandlers, method, phone, suffix string) *httptest.ResponseRecorder {
	r := mux.NewRouter()
	r.HandleFunc("/drivers/{phone}/archive", h.ArchiveDriver).Methods("POST")
	r.HandleFunc("/drivers/{phone}/restore", h.RestoreDriver).Methods("POST")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/drivers/"+phone+"/"+suffix, nil)
	r.ServeHTTP(rec, req)
	return rec
}

func TestArchiveDriver_BusyConflict(t *testing.T) {
	h := &AdminDriverHandlers{
		deService: &fakeArchiveDEService{err: service.ErrDEBusyArchive},
		logger:    logrus.New(),
	}
	rec := doDriverArchive(h, http.MethodPost, "+260971000001", "archive")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "DE_BUSY" {
		t.Fatalf("code = %q, want DE_BUSY", body.Error.Code)
	}
}

func TestArchiveDriver_OfflineOK(t *testing.T) {
	h := &AdminDriverHandlers{
		deService: &fakeArchiveDEService{de: &models.DeliveryExecutive{
			DEID:        "DE1",
			PhoneNumber: "+260971000001",
			Name:        "Ada",
			Status:      models.DEStatusOffline,
			Archived:    true,
			ArchivedAt:  "2026-09-09T12:00:00Z",
		}},
		logger: logrus.New(),
	}
	rec := doDriverArchive(h, http.MethodPost, "+260971000001", "archive")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["archived"] != true {
		t.Fatalf("archived = %v", body["archived"])
	}
	if body["de_id"] != "DE1" || body["phone_number"] != "+260971000001" {
		t.Fatalf("detail = %#v", body)
	}
	if _, ok := body["today_earnings_zmw"]; !ok {
		t.Fatal("200 must use GetDriver detail shape")
	}
}

func TestRestoreDriver_OK(t *testing.T) {
	h := &AdminDriverHandlers{
		deService: &fakeArchiveDEService{de: &models.DeliveryExecutive{
			DEID:        "DE1",
			PhoneNumber: "+260971000001",
			Status:      models.DEStatusOffline,
			Archived:    false,
		}},
		logger: logrus.New(),
	}
	rec := doDriverArchive(h, http.MethodPost, "+260971000001", "restore")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["archived"] != false {
		t.Fatalf("archived = %v", body["archived"])
	}
}

func TestRestoreDriver_MissingPhone(t *testing.T) {
	h := &AdminDriverHandlers{deService: &fakeArchiveDEService{}, logger: logrus.New()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/drivers/restore", nil)
	req = mux.SetURLVars(req, map[string]string{"phone": ""})
	h.RestoreDriver(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestArchiveDriver_NotFound(t *testing.T) {
	h := &AdminDriverHandlers{
		deService: &fakeArchiveDEService{err: repository.ErrDENotFound},
		logger:    logrus.New(),
	}
	rec := doDriverArchive(h, http.MethodPost, "+260971000001", "archive")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "DE_NOT_FOUND" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestArchiveDriver_InternalError(t *testing.T) {
	h := &AdminDriverHandlers{
		deService: &fakeArchiveDEService{err: errors.New("dynamo down")},
		logger:    logrus.New(),
	}
	rec := doDriverArchive(h, http.MethodPost, "+260971000001", "archive")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRestoreDriver_InternalError(t *testing.T) {
	h := &AdminDriverHandlers{
		deService: &fakeArchiveDEService{err: errors.New("dynamo down")},
		logger:    logrus.New(),
	}
	rec := doDriverArchive(h, http.MethodPost, "+260971000001", "restore")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestArchiveDriver_MissingPhone(t *testing.T) {
	h := &AdminDriverHandlers{deService: &fakeArchiveDEService{}, logger: logrus.New()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/drivers/archive", nil)
	req = mux.SetURLVars(req, map[string]string{"phone": ""})
	h.ArchiveDriver(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "MISSING_PARAM" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}
