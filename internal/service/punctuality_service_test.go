package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

func zTime(y, m, d, h, min int) time.Time {
	return time.Date(y, time.Month(m), d, h, min, 0, 0, timezone.ZambiaLocation())
}

type memPunctualityStore struct {
	mu      sync.Mutex
	records map[string]PunctualityRecord
}

func newMemPunctualityStore() *memPunctualityStore {
	return &memPunctualityStore{records: map[string]PunctualityRecord{}}
}

func (s *memPunctualityStore) Load(_ context.Context, phone string) (PunctualityRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[phone]
	if !ok {
		return PunctualityRecord{}, nil
	}
	return rec, nil
}

func (s *memPunctualityStore) Save(_ context.Context, phone string, rec PunctualityRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[phone] = rec
	return nil
}

func (s *memPunctualityStore) SaveShiftStart(ctx context.Context, phone string, rec PunctualityRecord) error {
	return s.Save(ctx, phone, rec)
}

func (s *memPunctualityStore) SaveScore(ctx context.Context, phone string, rec PunctualityRecord) error {
	return s.Save(ctx, phone, rec)
}

func (s *memPunctualityStore) SaveClear(ctx context.Context, phone string, rec PunctualityRecord) error {
	return s.Save(ctx, phone, rec)
}

func TestBackfillArrivals_ScoresAReturnAndLeavesASettledDayOutOfTheTotal(t *testing.T) {
	store := newMemPunctualityStore()
	phone := "+260971000001"
	setAt := zTime(2026, 6, 22, 6, 0)
	store.records[phone] = PunctualityRecord{
		ShiftStart:      "08:00",
		ShiftStartSetAt: setAt,
		Starts:          []SavedShiftStart{{Time: "08:00", SetAt: setAt}},
		Settlements: []PunctualitySettlement{{
			At: zTime(2026, 6, 22, 12, 0), Total: -20, Note: "cut", Admin: "ada",
		}},
	}
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 23, 8, 0) }, logrus.New())
	n, err := svc.BackfillArrivals(context.Background(), phone, []ArrivalScan{
		{At: zTime(2026, 6, 22, 8, 13), Reason: "scan_return"},
		{At: zTime(2026, 6, 23, 8, 5), Reason: "scan_start"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("scored %d days, want 2", n)
	}
	rec, err := store.Load(context.Background(), phone)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Days) != 2 || rec.Days[0].FirstScan != "08:13" || rec.Days[0].SignedMinutes != -13 {
		t.Fatalf("days = %+v", rec.Days)
	}
	if rec.OpenMinutes != -5 {
		t.Fatalf("open minutes = %d, want -5 from the day after the clear", rec.OpenMinutes)
	}
}

func TestBackfillArrivals_DoesNotRewriteAScoredDay(t *testing.T) {
	store := newMemPunctualityStore()
	phone := "+260971000001"
	setAt := zTime(2026, 6, 22, 6, 0)
	store.records[phone] = PunctualityRecord{
		ShiftStart:      "08:00",
		ShiftStartSetAt: setAt,
		Starts:          []SavedShiftStart{{Time: "08:00", SetAt: setAt}},
		OpenMinutes:     -20,
		Days: []PunctualityDay{{
			Date: "2026-06-22", ShiftStart: "08:00", FirstScan: "08:20", SignedMinutes: -20, ScoredAt: zTime(2026, 6, 22, 8, 20),
		}},
	}
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 18, 0) }, logrus.New())
	n, err := svc.BackfillArrivals(context.Background(), phone, []ArrivalScan{
		{At: zTime(2026, 6, 22, 8, 13), Reason: "scan_return"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("scored %d days, want 0", n)
	}
	rec, err := store.Load(context.Background(), phone)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Days) != 1 || rec.Days[0].FirstScan != "08:20" || rec.Days[0].SignedMinutes != -20 || rec.OpenMinutes != -20 {
		t.Fatalf("rewrote scored day: %+v open=%d", rec.Days, rec.OpenMinutes)
	}
}

func TestBackfillArrivals_AddsAnUnusedStartWithoutRewritingTheFirst(t *testing.T) {
	store := newMemPunctualityStore()
	setAt := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)
	clearedAt := time.Date(2026, 6, 22, 16, 0, 0, 0, time.UTC)
	store.records["+260971000001"] = PunctualityRecord{
		Starts: []SavedShiftStart{
			{Time: "08:00", SetAt: setAt},
			{Time: "16:00", SetAt: setAt},
		},
		Days: []PunctualityDay{{
			Date: "2026-06-22", ShiftStart: "08:00", FirstScan: "08:10",
			SignedMinutes: -10, ScoredAt: time.Date(2026, 6, 22, 6, 10, 0, 0, time.UTC),
		}},
		Settlements: []PunctualitySettlement{{At: clearedAt, Total: -10, Note: "cut"}},
		OpenMinutes: 0,
	}
	svc := NewPunctualityService(store, func() time.Time { return clearedAt }, logrus.New())
	n, err := svc.BackfillArrivals(context.Background(), "+260971000001", []ArrivalScan{
		{At: time.Date(2026, 6, 22, 14, 20, 0, 0, time.UTC), Reason: "scan_return"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("new rows = %d, want 1", n)
	}
	rec := store.records["+260971000001"]
	if rec.Days[0].ShiftStart != "08:00" || rec.Days[0].SignedMinutes != -10 {
		t.Fatalf("rewrote the first score: %+v", rec.Days[0])
	}
	if rec.Days[1].ShiftStart != "16:00" || rec.Days[1].SignedMinutes != -20 {
		t.Fatalf("second score: %+v", rec.Days[1])
	}
	if rec.OpenMinutes != 0 {
		t.Fatalf("open = %d, want 0 because 16:20 is before the clear", rec.OpenMinutes)
	}
}

func TestAddShiftStart_StoresCanonicalTimesEarliestFirst(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 6, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.AddShiftStart(ctx, phone, "16:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddShiftStart(ctx, phone, "8:00"); err != nil {
		t.Fatal(err)
	}
	rec, err := store.Load(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Starts) != 2 || rec.Starts[0].Time != "08:00" || rec.Starts[1].Time != "16:00" {
		t.Fatalf("starts = %+v", rec.Starts)
	}
	if rec.ShiftStart != "08:00" {
		t.Fatalf("legacy start = %s", rec.ShiftStart)
	}
}

func TestAddShiftStart_ScoresTheClosestSavedTime(t *testing.T) {
	store := newMemPunctualityStore()
	saved := zTime(2026, 6, 22, 6, 0)
	svc := NewPunctualityService(store, func() time.Time { return saved }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.AddShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddShiftStart(ctx, phone, "16:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 16, 5)); err != nil {
		t.Fatal(err)
	}
	day := store.records[phone].Days[0]
	if day.ShiftStart != "16:00" || day.SignedMinutes != -5 {
		t.Fatalf("day = %+v", day)
	}
}

func TestRemoveShiftStart_RefusesTheLastTime(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 6, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.AddShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddShiftStart(ctx, phone, "16:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveShiftStart(ctx, phone, "16:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveShiftStart(ctx, phone, "08:00"); err == nil {
		t.Fatal("expected removing the last shift start to be rejected")
	}
	if len(store.records[phone].Starts) != 1 || store.records[phone].Starts[0].Time != "08:00" {
		t.Fatalf("starts = %+v", store.records[phone].Starts)
	}
}

func TestReplaceShiftStart_AppliesOnlyToLaterScans(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 6, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.AddShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 8, 20)); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReplaceShiftStart(ctx, phone, "08:00", "09:00"); err != nil {
		t.Fatal(err)
	}
	if store.records[phone].Days[0].ShiftStart != "08:00" || store.records[phone].Days[0].SignedMinutes != -20 {
		t.Fatalf("scored day changed: %+v", store.records[phone].Days[0])
	}
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 23, 9, 10)); err != nil {
		t.Fatal(err)
	}
	if store.records[phone].Days[1].ShiftStart != "09:00" || store.records[phone].Days[1].SignedMinutes != -10 {
		t.Fatalf("next day = %+v", store.records[phone].Days[1])
	}
}

func TestSetShiftStart_SavesZambiaTime(t *testing.T) {
	store := newMemPunctualityStore()
	now := zTime(2026, 6, 22, 6, 0)
	svc := NewPunctualityService(store, func() time.Time { return now }, logrus.New())

	if err := svc.SetShiftStart(context.Background(), "+260971000001", "08:00"); err != nil {
		t.Fatal(err)
	}
	rec := store.records["+260971000001"]
	if rec.ShiftStart != "08:00" {
		t.Fatalf("shift start = %q, want 08:00", rec.ShiftStart)
	}
	if !rec.ShiftStartSetAt.Equal(now) {
		t.Fatalf("set at = %s, want %s", rec.ShiftStartSetAt, now)
	}
}

func TestSetShiftStart_RejectsBlankAndBadClock(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 6, 0) }, logrus.New())
	ctx := context.Background()
	if err := svc.SetShiftStart(ctx, "+260971000001", "08:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetShiftStart(ctx, "+260971000001", "  "); err == nil {
		t.Fatal("expected blank shift start to be rejected")
	}
	if err := svc.SetShiftStart(ctx, "+260971000001", "8am"); err == nil {
		t.Fatal("expected a bad clock to be rejected")
	}
	if store.records["+260971000001"].ShiftStart != "08:00" {
		t.Fatal("rejected update must leave the saved time in place")
	}
}

func TestRecordScan_ScoresLateFirstScanStart(t *testing.T) {
	store := newMemPunctualityStore()
	setAt := zTime(2026, 6, 22, 6, 0)
	svc := NewPunctualityService(store, func() time.Time { return setAt }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.SetShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	scanAt := zTime(2026, 6, 22, 8, 20)
	if err := svc.RecordScan(ctx, phone, "scan_start", scanAt); err != nil {
		t.Fatal(err)
	}
	rec := store.records[phone]
	if rec.OpenMinutes != -20 {
		t.Fatalf("open minutes = %d, want -20", rec.OpenMinutes)
	}
	if len(rec.Days) != 1 || rec.Days[0].Date != "2026-06-22" || rec.Days[0].FirstScan != "08:20" || rec.Days[0].ShiftStart != "08:00" || rec.Days[0].SignedMinutes != -20 {
		t.Fatalf("days = %+v", rec.Days)
	}
	if rec.LastScoredDate != "2026-06-22" || rec.LastScoredMinutes != -20 {
		t.Fatalf("last scored = %s %d", rec.LastScoredDate, rec.LastScoredMinutes)
	}
}

func TestRecordScan_ReturnScoresAndLaterScansDoNot(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 6, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.SetShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	// Scan that happened before the shift start was saved.
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 5, 30)); err != nil {
		t.Fatal(err)
	}
	if len(store.records[phone].Days) != 0 {
		t.Fatal("scan before the shift start was saved must not score")
	}
	if err := svc.RecordScan(ctx, phone, "scan_return", zTime(2026, 6, 22, 7, 50)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 8, 20)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordScan(ctx, phone, "scan_return_while_busy", zTime(2026, 6, 22, 14, 0)); err != nil {
		t.Fatal(err)
	}
	rec := store.records[phone]
	if len(rec.Days) != 1 || rec.OpenMinutes != 10 || rec.Days[0].FirstScan != "07:50" {
		t.Fatalf("record = %+v", rec.Days)
	}
}

func TestRecordScan_EarlyDayReducesOpenTotal(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 6, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.SetShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 8, 20)); err != nil {
		t.Fatal(err)
	}
	// Next day the clock and the saved shift start are already in the past relative to the scan.
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 23, 7, 40)); err != nil {
		t.Fatal(err)
	}
	if store.records[phone].OpenMinutes != 0 {
		t.Fatalf("open minutes = %d, want 0", store.records[phone].OpenMinutes)
	}
}

func TestRecordScan_SecondStartAddsARowAndSumsTheDay(t *testing.T) {
	store := newMemPunctualityStore()
	setAt := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)
	store.records["+260971000001"] = PunctualityRecord{
		Starts: []SavedShiftStart{
			{Time: "08:00", SetAt: setAt},
			{Time: "16:00", SetAt: setAt},
		},
		Days: []PunctualityDay{{
			Date: "2026-06-22", ShiftStart: "08:00", FirstScan: "08:10",
			SignedMinutes: -10, ScoredAt: time.Date(2026, 6, 22, 6, 10, 0, 0, time.UTC),
		}},
		OpenMinutes: -10, LastScoredDate: "2026-06-22", LastScoredMinutes: -10,
	}
	svc := NewPunctualityService(store, func() time.Time { return setAt }, logrus.New())
	scan := time.Date(2026, 6, 22, 13, 0, 0, 0, time.UTC) // 15:00 Zambia
	if err := svc.RecordScan(context.Background(), "+260971000001", "scan_return", scan); err != nil {
		t.Fatal(err)
	}
	rec := store.records["+260971000001"]
	if len(rec.Days) != 2 || rec.Days[0].SignedMinutes != -10 {
		t.Fatalf("first score changed: %+v", rec.Days)
	}
	if rec.Days[1].ShiftStart != "16:00" || rec.Days[1].SignedMinutes != 60 {
		t.Fatalf("second score: %+v", rec.Days[1])
	}
	if rec.LastScoredMinutes != 50 || rec.OpenMinutes != 50 {
		t.Fatalf("day sum and open = %d / %d, want 50 / 50", rec.LastScoredMinutes, rec.OpenMinutes)
	}
}

func TestClear_ResetsOpenTotalAndKeepsTheDay(t *testing.T) {
	store := newMemPunctualityStore()
	clearedAt := zTime(2026, 6, 22, 18, 0)
	svc := NewPunctualityService(store, func() time.Time { return clearedAt }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.SetShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	// Shift was saved at 18:00 in this clock, so backdate the set time to before the scan.
	rec := store.records[phone]
	rec.ShiftStartSetAt = zTime(2026, 6, 22, 6, 0)
	for i := range rec.Starts {
		rec.Starts[i].SetAt = rec.ShiftStartSetAt
	}
	store.records[phone] = rec
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 8, 20)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Clear(ctx, phone, "deducted K20", "ada"); err != nil {
		t.Fatal(err)
	}
	rec = store.records[phone]
	if rec.OpenMinutes != 0 {
		t.Fatalf("open minutes = %d, want 0", rec.OpenMinutes)
	}
	if len(rec.Days) != 1 || rec.Days[0].SignedMinutes != -20 {
		t.Fatalf("day was not kept: %+v", rec.Days)
	}
	if len(rec.Settlements) != 1 || rec.Settlements[0].Total != -20 || rec.Settlements[0].Note != "deducted K20" || rec.Settlements[0].Admin != "ada" || !rec.Settlements[0].At.Equal(clearedAt) {
		t.Fatalf("settlement = %+v", rec.Settlements)
	}
}

func TestClearAmount_LeavesRemainderAndDoesNotSettleTheDay(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 18, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.SetShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	rec := store.records[phone]
	rec.ShiftStartSetAt = zTime(2026, 6, 22, 6, 0)
	for i := range rec.Starts {
		rec.Starts[i].SetAt = rec.ShiftStartSetAt
	}
	store.records[phone] = rec
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 8, 20)); err != nil {
		t.Fatal(err)
	}
	if err := svc.ClearAmount(ctx, phone, 0, 5, "cut five minutes", "ada"); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Report(ctx, phone, "2026-06-22")
	if err != nil {
		t.Fatal(err)
	}
	if report.OpenMinutes != -15 {
		t.Fatalf("open minutes = %d, want -15", report.OpenMinutes)
	}
	if report.Day == nil || report.Day.Settled {
		t.Fatalf("day = %+v, want unsettled", report.Day)
	}
	if len(report.Settlements) != 1 || report.Settlements[0].Total != -5 || report.Settlements[0].Note != "cut five minutes" {
		t.Fatalf("settlements = %+v", report.Settlements)
	}

	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 23, 8, 10)); err != nil {
		t.Fatal(err)
	}
	report, err = svc.Report(ctx, phone, "2026-06-23")
	if err != nil {
		t.Fatal(err)
	}
	if report.OpenMinutes != -25 {
		t.Fatalf("open minutes after the next day = %d, want -25", report.OpenMinutes)
	}
	if report.Day == nil || report.Day.Settled || report.Day.SignedMinutes != -10 {
		t.Fatalf("next day = %+v", report.Day)
	}
}

func TestClearAmount_ExactAmountSettlesTheDay(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 18, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.SetShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	rec := store.records[phone]
	rec.ShiftStartSetAt = zTime(2026, 6, 22, 6, 0)
	for i := range rec.Starts {
		rec.Starts[i].SetAt = rec.ShiftStartSetAt
	}
	store.records[phone] = rec
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 8, 20)); err != nil {
		t.Fatal(err)
	}
	if err := svc.ClearAmount(ctx, phone, 0, 20, "cleared the rest", "ada"); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Report(ctx, phone, "2026-06-22")
	if err != nil {
		t.Fatal(err)
	}
	if report.OpenMinutes != 0 || report.Day == nil || !report.Day.Settled {
		t.Fatalf("report = %+v day=%+v", report, report.Day)
	}
	if len(report.Settlements) != 1 || report.Settlements[0].Total != -20 {
		t.Fatalf("settlements = %+v", report.Settlements)
	}
	if store.records[phone].Settlements[0].Partial {
		t.Fatal("exact amount was stored as a partial clear")
	}
}

func TestClear_RejectsZeroAndBlankNote(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 18, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.Clear(ctx, phone, "nothing to settle", "ada"); err == nil {
		t.Fatal("expected a zero aggregate to be rejected")
	}
	if err := svc.SetShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	rec := store.records[phone]
	rec.ShiftStartSetAt = zTime(2026, 6, 22, 6, 0)
	for i := range rec.Starts {
		rec.Starts[i].SetAt = rec.ShiftStartSetAt
	}
	store.records[phone] = rec
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 7, 40)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Clear(ctx, phone, "   ", "ada"); err == nil {
		t.Fatal("expected a blank note to be rejected")
	}
	if store.records[phone].OpenMinutes != 20 {
		t.Fatalf("rejected clear changed the total: %d", store.records[phone].OpenMinutes)
	}
	if err := svc.Clear(ctx, phone, "reset early credit", "ada"); err != nil {
		t.Fatal(err)
	}
	if store.records[phone].OpenMinutes != 0 || store.records[phone].Settlements[0].Total != 20 {
		t.Fatalf("early clear = %+v", store.records[phone].Settlements)
	}
}

func TestReport_ShowsOpenTotalAndSettledDay(t *testing.T) {
	store := newMemPunctualityStore()
	svc := NewPunctualityService(store, func() time.Time { return zTime(2026, 6, 22, 18, 0) }, logrus.New())
	ctx := context.Background()
	phone := "+260971000001"
	if err := svc.SetShiftStart(ctx, phone, "08:00"); err != nil {
		t.Fatal(err)
	}
	rec := store.records[phone]
	rec.ShiftStartSetAt = zTime(2026, 6, 22, 6, 0)
	for i := range rec.Starts {
		rec.Starts[i].SetAt = rec.ShiftStartSetAt
	}
	store.records[phone] = rec
	if err := svc.RecordScan(ctx, phone, "scan_start", zTime(2026, 6, 22, 8, 20)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Clear(ctx, phone, "deducted K20", "ada"); err != nil {
		t.Fatal(err)
	}

	report, err := svc.Report(ctx, phone, "2026-06-22")
	if err != nil {
		t.Fatal(err)
	}
	if report.ShiftStart != "08:00" || report.OpenMinutes != 0 || report.Date != "2026-06-22" {
		t.Fatalf("report header = %+v", report)
	}
	if report.Day == nil || report.Day.SignedMinutes != -20 || !report.Day.Settled || report.Day.FirstScan != "08:20" {
		t.Fatalf("day = %+v", report.Day)
	}
	if len(report.Settlements) != 1 || report.Settlements[0].Note != "deducted K20" || report.Settlements[0].Total != -20 || report.Settlements[0].Admin != "ada" {
		t.Fatalf("settlements = %+v", report.Settlements)
	}

	next, err := svc.Report(ctx, phone, "2026-06-23")
	if err != nil {
		t.Fatal(err)
	}
	if next.Day != nil {
		t.Fatalf("unscored day = %+v", next.Day)
	}
	if next.OpenMinutes != 0 {
		t.Fatalf("open minutes = %d", next.OpenMinutes)
	}
}

type fakeScanRecorder struct {
	phone  string
	reason string
	at     time.Time
	err    error
}

func (f *fakeScanRecorder) RecordScan(_ context.Context, phone, reason string, scanAt time.Time) error {
	f.phone = phone
	f.reason = reason
	f.at = scanAt
	return f.err
}

func TestRecordPunctuality_ForwardsTheScan(t *testing.T) {
	rec := &fakeScanRecorder{}
	svc := &DEService{punctuality: rec, logger: logrus.New()}
	at := zTime(2026, 6, 22, 8, 20)
	svc.recordPunctuality(context.Background(), "+260971000001", "scan_start", at)
	if rec.phone != "+260971000001" || rec.reason != "scan_start" || !rec.at.Equal(at) {
		t.Fatalf("recorded %+v", rec)
	}
}

func TestRecordPunctuality_IgnoresRecorderFailure(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	svc := &DEService{punctuality: &fakeScanRecorder{err: errors.New("dynamo down")}, logger: logger}
	svc.recordPunctuality(context.Background(), "+260971000001", "scan_start", zTime(2026, 6, 22, 8, 20))
}
