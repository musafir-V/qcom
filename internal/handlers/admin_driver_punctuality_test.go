package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/punctuality"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type fakePunctuality struct {
	report   *service.PunctualityReport
	err      error
	shift    string
	from     string
	note     string
	admin    string
	hours    int
	minutes  int
	partial  bool
	gotPhone string
	gotDate  string
}

func (f *fakePunctuality) SetShiftStart(_ context.Context, phone, hhmm string) error {
	f.gotPhone = phone
	f.shift = hhmm
	return f.err
}

func (f *fakePunctuality) AddShiftStart(_ context.Context, phone, hhmm string) error {
	f.gotPhone = phone
	f.shift = hhmm
	return f.err
}

func (f *fakePunctuality) ReplaceShiftStart(_ context.Context, phone, from, to string) error {
	f.gotPhone = phone
	f.from = from
	f.shift = to
	return f.err
}

func (f *fakePunctuality) RemoveShiftStart(_ context.Context, phone, hhmm string) error {
	f.gotPhone = phone
	f.shift = hhmm
	return f.err
}

func (f *fakePunctuality) Clear(_ context.Context, phone, note, admin string) error {
	f.gotPhone = phone
	f.note = note
	f.admin = admin
	return f.err
}

func (f *fakePunctuality) ClearAmount(_ context.Context, phone string, hours, minutes int, note, admin string) error {
	f.gotPhone = phone
	f.hours = hours
	f.minutes = minutes
	f.note = note
	f.admin = admin
	f.partial = true
	return f.err
}

func (f *fakePunctuality) Report(_ context.Context, phone, date string) (*service.PunctualityReport, error) {
	f.gotPhone = phone
	f.gotDate = date
	return f.report, f.err
}

func doPunctuality(h *AdminDriverHandlers, method, phone, suffix, body string) *httptest.ResponseRecorder {
	r := mux.NewRouter()
	r.HandleFunc("/drivers/{phone}/punctuality", h.GetDriverPunctuality).Methods("GET")
	r.HandleFunc("/drivers/{phone}/shift-start", h.SetDriverShiftStart).Methods("PUT")
	r.HandleFunc("/drivers/{phone}/shift-starts", h.AddDriverShiftStart).Methods("POST")
	r.HandleFunc("/drivers/{phone}/shift-starts", h.ReplaceDriverShiftStart).Methods("PUT")
	r.HandleFunc("/drivers/{phone}/shift-starts", h.RemoveDriverShiftStart).Methods("DELETE")
	r.HandleFunc("/drivers/{phone}/punctuality/clear", h.ClearDriverPunctuality).Methods("POST")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/drivers/"+phone+"/"+suffix, strings.NewReader(body))
	r.ServeHTTP(rec, req)
	return rec
}

func TestGetDriverPunctuality_ReturnsOpenTotalAndDay(t *testing.T) {
	fake := &fakePunctuality{report: &service.PunctualityReport{
		ShiftStart:  "08:00",
		Date:        "2026-06-22",
		OpenMinutes: -20,
		DayStarts: []service.PunctualityDayView{{
			ShiftStart: "08:00", FirstScan: "08:20", SignedMinutes: -20,
		}},
		Settlements: []service.PunctualitySettlementView{},
	}}
	h := &AdminDriverHandlers{punctuality: fake, logger: logrus.New()}
	rec := doPunctuality(h, http.MethodGet, "+260971000001", "punctuality?date=2026-06-22", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.gotPhone != "+260971000001" || fake.gotDate != "2026-06-22" {
		t.Fatalf("phone=%s date=%s", fake.gotPhone, fake.gotDate)
	}
	var body service.PunctualityReport
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OpenMinutes != -20 || len(body.DayStarts) != 1 || body.DayStarts[0].SignedMinutes != -20 {
		t.Fatalf("body = %+v", body)
	}
}

func TestClearDriverPunctuality_RejectsZero(t *testing.T) {
	h := &AdminDriverHandlers{
		punctuality: &fakePunctuality{err: punctuality.ErrClearZero},
		logger:      logrus.New(),
	}
	rec := doPunctuality(h, http.MethodPost, "+260971000001", "punctuality/clear", `{"note":"cut"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "NOTHING_TO_CLEAR" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestSetDriverShiftStart_SavesTime(t *testing.T) {
	fake := &fakePunctuality{}
	h := &AdminDriverHandlers{punctuality: fake, logger: logrus.New()}
	req := httptest.NewRequest(http.MethodPut, "/drivers/+260971000001/shift-start", strings.NewReader(`{"shift_start":"08:00"}`))
	req = req.WithContext(context.WithValue(req.Context(), "entity_id", "ada"))
	r := mux.NewRouter()
	r.HandleFunc("/drivers/{phone}/shift-start", h.SetDriverShiftStart).Methods("PUT")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.shift != "08:00" || fake.gotPhone != "+260971000001" {
		t.Fatalf("saved phone=%s shift=%s", fake.gotPhone, fake.shift)
	}
}

func TestAddDriverShiftStart_SavesAnotherTime(t *testing.T) {
	fake := &fakePunctuality{}
	h := &AdminDriverHandlers{punctuality: fake, logger: logrus.New()}
	rec := doPunctuality(h, http.MethodPost, "+260971000001", "shift-starts", `{"shift_start":"16:00"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.shift != "16:00" || fake.gotPhone != "+260971000001" {
		t.Fatalf("saved phone=%s shift=%s", fake.gotPhone, fake.shift)
	}
}

func TestReplaceDriverShiftStart_SendsFromAndTo(t *testing.T) {
	fake := &fakePunctuality{}
	h := &AdminDriverHandlers{punctuality: fake, logger: logrus.New()}
	rec := doPunctuality(h, http.MethodPut, "+260971000001", "shift-starts", `{"from":"08:00","to":"09:00"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.from != "08:00" || fake.shift != "09:00" {
		t.Fatalf("from=%s to=%s", fake.from, fake.shift)
	}
}

func TestRemoveDriverShiftStart_RejectsTheLastTime(t *testing.T) {
	h := &AdminDriverHandlers{
		punctuality: &fakePunctuality{err: service.ErrLastShiftStart},
		logger:      logrus.New(),
	}
	rec := doPunctuality(h, http.MethodDelete, "+260971000001", "shift-starts", `{"shift_start":"08:00"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "LAST_SHIFT_START" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestClearDriverPunctuality_ClearsTypedHoursAndMinutes(t *testing.T) {
	fake := &fakePunctuality{}
	h := &AdminDriverHandlers{punctuality: fake, logger: logrus.New()}
	req := httptest.NewRequest(http.MethodPost, "/drivers/+260971000001/punctuality/clear", strings.NewReader(`{"note":"cut five","hours":1,"minutes":30}`))
	req = req.WithContext(context.WithValue(req.Context(), "entity_id", "ada"))
	r := mux.NewRouter()
	r.HandleFunc("/drivers/{phone}/punctuality/clear", h.ClearDriverPunctuality).Methods("POST")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !fake.partial || fake.hours != 1 || fake.minutes != 30 || fake.note != "cut five" || fake.admin != "ada" {
		t.Fatalf("partial=%v hours=%d minutes=%d note=%q admin=%q", fake.partial, fake.hours, fake.minutes, fake.note, fake.admin)
	}
}

func TestClearDriverPunctuality_RecordsAdminAndNote(t *testing.T) {
	fake := &fakePunctuality{}
	h := &AdminDriverHandlers{punctuality: fake, logger: logrus.New()}
	req := httptest.NewRequest(http.MethodPost, "/drivers/+260971000001/punctuality/clear", strings.NewReader(`{"note":"deducted K20"}`))
	req = req.WithContext(context.WithValue(req.Context(), "entity_id", "ada"))
	r := mux.NewRouter()
	r.HandleFunc("/drivers/{phone}/punctuality/clear", h.ClearDriverPunctuality).Methods("POST")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.note != "deducted K20" || fake.admin != "ada" {
		t.Fatalf("note=%q admin=%q", fake.note, fake.admin)
	}
	if errors.Is(fake.err, punctuality.ErrClearZero) {
		t.Fatal("unexpected")
	}
}
