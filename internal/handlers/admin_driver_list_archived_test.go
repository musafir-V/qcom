package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type fakeListDEService struct {
	des        []*models.DeliveryExecutive
	gotInclude bool
	sawInclude bool
}

func (f *fakeListDEService) ListDriversByStore(_ context.Context, _, _, _ string, _ int32, includeArchived bool) ([]*models.DeliveryExecutive, string, error) {
	f.gotInclude = includeArchived
	f.sawInclude = true
	if includeArchived {
		return f.des, "", nil
	}
	out := make([]*models.DeliveryExecutive, 0, len(f.des))
	for _, de := range f.des {
		if de != nil && !de.Archived {
			out = append(out, de)
		}
	}
	return out, "", nil
}

func (f *fakeListDEService) GetTodayEarnings(context.Context, string) (float64, error) {
	return 0, nil
}
func (f *fakeListDEService) Register(context.Context, service.RegisterDERequest) (*models.DeliveryExecutive, error) {
	return nil, nil
}
func (f *fakeListDEService) ReassignStore(context.Context, string, string) error { return nil }

func listDriversSample() []*models.DeliveryExecutive {
	return []*models.DeliveryExecutive{
		{DEID: "DE1", PhoneNumber: "+260971000001", Name: "Active", Status: models.DEStatusOffline},
		{DEID: "DE2", PhoneNumber: "+260971000002", Name: "Archived", Status: models.DEStatusOffline, Archived: true, ArchivedAt: "2026-09-09T12:00:00Z"},
	}
}

func doListDrivers(h *AdminDriverHandlers, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ListDrivers(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/drivers?"+query, nil))
	return rec
}

func TestListDrivers_DefaultExcludesArchived(t *testing.T) {
	svc := &fakeListDEService{des: listDriversSample()}
	h := &AdminDriverHandlers{deService: svc, logger: logrus.New()}

	rec := doListDrivers(h, "assigned_store_id=221")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.sawInclude || svc.gotInclude {
		t.Fatalf("include_archived passed = %v, want false", svc.gotInclude)
	}
	var body struct {
		Drivers []map[string]interface{} `json:"drivers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Drivers) != 1 || body.Drivers[0]["de_id"] != "DE1" {
		t.Fatalf("drivers = %#v, want only DE1", body.Drivers)
	}
}

func TestListDrivers_IncludeArchivedTrue(t *testing.T) {
	svc := &fakeListDEService{des: listDriversSample()}
	h := &AdminDriverHandlers{deService: svc, logger: logrus.New()}

	rec := doListDrivers(h, "assigned_store_id=221&include_archived=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.gotInclude {
		t.Fatal("include_archived=true must be passed through")
	}
	var body struct {
		Drivers []map[string]interface{} `json:"drivers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Drivers) != 2 {
		t.Fatalf("drivers = %#v, want both", body.Drivers)
	}
	var archived map[string]interface{}
	for _, d := range body.Drivers {
		if d["de_id"] == "DE2" {
			archived = d
		}
	}
	if archived == nil {
		t.Fatal("expected archived driver in list")
	}
	if archived["archived"] != true {
		t.Fatalf("archived field = %v", archived["archived"])
	}
	if archived["archived_at"] != "2026-09-09T12:00:00Z" {
		t.Fatalf("archived_at = %v", archived["archived_at"])
	}
}

func TestParseIncludeArchived(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"false", false},
		{"0", false},
		{"true", true},
		{"TRUE", true},
		{"1", true},
	}
	for _, c := range cases {
		if got := parseIncludeArchived(c.in); got != c.want {
			t.Fatalf("parseIncludeArchived(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGetDriver_ExposesArchivedFields(t *testing.T) {
	de := &models.DeliveryExecutive{
		DEID:        "DE2",
		PhoneNumber: "+260971000002",
		Name:        "Archived",
		Status:      models.DEStatusOffline,
		Archived:    true,
		ArchivedAt:  "2026-09-09T12:00:00Z",
	}
	body := driverDetail(de, 0)
	if body["archived"] != true {
		t.Fatalf("archived = %v", body["archived"])
	}
	if body["archived_at"] != "2026-09-09T12:00:00Z" {
		t.Fatalf("archived_at = %v", body["archived_at"])
	}
	if body["de_id"] != "DE2" || body["phone_number"] != "+260971000002" {
		t.Fatalf("detail = %#v", body)
	}
}
