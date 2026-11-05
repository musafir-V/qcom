package service

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

type stubPresenceEvents struct {
	events []*models.DEStatusEvent
}

func (s stubPresenceEvents) ListEventsForDay(context.Context, string, string) ([]*models.DEStatusEvent, error) {
	return s.events, nil
}

func presenceLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func zambiaTS(date, hhmm string) string {
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+hhmm, timezone.ZambiaLocation())
	if err != nil {
		panic(err)
	}
	return t.UTC().Format(time.RFC3339)
}

func firstScanPresence(t *testing.T, date string, events []*models.DEStatusEvent) *PresenceReport {
	t.Helper()
	svc := NewPresenceService(stubPresenceEvents{events: events}, presenceLogger())
	report, err := svc.GetDayPresence(context.Background(), "+260971000001", date)
	if err != nil {
		t.Fatalf("GetDayPresence: %v", err)
	}
	return report
}

func assertFirstScan(t *testing.T, report *PresenceReport, want string) {
	t.Helper()
	if want == "" {
		if report.FirstScanAt != nil {
			t.Fatalf("FirstScanAt = %q, want nil", *report.FirstScanAt)
		}
		return
	}
	if report.FirstScanAt == nil || *report.FirstScanAt != want {
		t.Fatalf("FirstScanAt = %v, want %s", report.FirstScanAt, want)
	}
}

func TestGetDayPresence_FirstScanAt_EarliestStoreQR(t *testing.T) {
	const date = "2026-11-05"
	report := firstScanPresence(t, date, []*models.DEStatusEvent{{
		FromState: models.DEStatusOffline,
		ToState:   models.DEStatusEligible,
		Reason:    models.ReasonScanStart,
		StoreID:   "221",
		TS:        zambiaTS(date, "07:42"),
	}})
	assertFirstScan(t, report, "07:42")
}

func TestGetDayPresence_FirstScanAt_CountsScanReturn(t *testing.T) {
	const date = "2026-11-05"
	report := firstScanPresence(t, date, []*models.DEStatusEvent{{
		FromState: models.DEStatusFree,
		ToState:   models.DEStatusEligible,
		Reason:    models.ReasonScanReturn,
		StoreID:   "221",
		TS:        zambiaTS(date, "09:15"),
	}})
	assertFirstScan(t, report, "09:15")
}

func TestGetDayPresence_FirstScanAt_IgnoresNonScanEvents(t *testing.T) {
	const date = "2026-11-05"
	report := firstScanPresence(t, date, []*models.DEStatusEvent{
		{
			FromState: models.DEStatusEligible,
			ToState:   models.DEStatusBusy,
			Reason:    models.ReasonAssigned,
			TS:        zambiaTS(date, "06:00"),
		},
		{
			FromState: models.DEStatusOffline,
			ToState:   models.DEStatusEligible,
			Reason:    models.ReasonScanStart,
			TS:        zambiaTS(date, "07:42"),
		},
		{
			FromState: models.DEStatusFree,
			ToState:   models.DEStatusEligible,
			Reason:    models.ReasonScanReturn,
			TS:        zambiaTS(date, "11:00"),
		},
	})
	assertFirstScan(t, report, "07:42")
}

func TestGetDayPresence_FirstScanAt_NilWhenNoStoreQR(t *testing.T) {
	const date = "2026-11-05"
	report := firstScanPresence(t, date, []*models.DEStatusEvent{{
		FromState: models.DEStatusEligible,
		ToState:   models.DEStatusOffline,
		Reason:    models.ReasonEndedDuty,
		TS:        zambiaTS(date, "08:00"),
	}})
	assertFirstScan(t, report, "")
}

func TestGetDayPresence_FirstScanAt_NotMidnightWhenAlreadyOnline(t *testing.T) {
	const date = "2026-11-05"
	report := firstScanPresence(t, date, []*models.DEStatusEvent{{
		FromState: models.DEStatusEligible,
		ToState:   models.DEStatusOffline,
		Reason:    models.ReasonEndedDuty,
		StoreID:   "221",
		TS:        zambiaTS(date, "11:15"),
	}})
	if len(report.Segments) != 1 || report.Segments[0].Start != "00:00" {
		t.Fatalf("segments = %+v, want one starting 00:00", report.Segments)
	}
	assertFirstScan(t, report, "")
}
