package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/punctuality"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

// ErrLastShiftStart is returned when removing the driver's only start time.
var ErrLastShiftStart = errors.New("the last shift start cannot be removed")

// ErrDuplicateShiftStart is returned when a start time is already on the list.
var ErrDuplicateShiftStart = errors.New("shift start already exists")

// ErrShiftStartNotFound is returned when replacing or removing a missing time.
var ErrShiftStartNotFound = errors.New("shift start not found")

// SavedShiftStart is one expected arrival and when it was saved.
type SavedShiftStart struct {
	Time  string
	SetAt time.Time
}

// PunctualityRecord is one driver's shift starts, scored days, and clears.
type PunctualityRecord struct {
	ShiftStart        string
	ShiftStartSetAt   time.Time
	Starts            []SavedShiftStart
	OpenMinutes       int
	LastScoredDate    string
	LastScoredMinutes int
	Days              []PunctualityDay
	Settlements       []PunctualitySettlement
}

// PunctualityDay is the frozen score for one Zambia date.
type PunctualityDay struct {
	Date          string
	ShiftStart    string
	FirstScan     string
	SignedMinutes int
	ScoredAt      time.Time
}

// PunctualitySettlement is one clear of the open aggregate.
type PunctualitySettlement struct {
	At    time.Time
	Total int
	Note  string
	Admin string
}

// PunctualityStore persists a driver's punctuality record.
type PunctualityStore interface {
	Load(ctx context.Context, phone string) (PunctualityRecord, error)
	SaveShiftStart(ctx context.Context, phone string, rec PunctualityRecord) error
	SaveScore(ctx context.Context, phone string, rec PunctualityRecord) error
	SaveClear(ctx context.Context, phone string, rec PunctualityRecord) error
}

// PunctualityService scores driver arrival against a shift start time.
type PunctualityService struct {
	store  PunctualityStore
	now    func() time.Time
	logger *logrus.Logger
}

func NewPunctualityService(store PunctualityStore, now func() time.Time, logger *logrus.Logger) *PunctualityService {
	return &PunctualityService{store: store, now: now, logger: logger}
}

// SetShiftStart adds one expected arrival time. A blank or duplicate time is rejected.
func (s *PunctualityService) SetShiftStart(ctx context.Context, phone, hhmm string) error {
	return s.AddShiftStart(ctx, phone, hhmm)
}

// AddShiftStart appends a start time. It applies only to scans after now.
func (s *PunctualityService) AddShiftStart(ctx context.Context, phone, hhmm string) error {
	hhmm, err := normalizeShiftStart(hhmm)
	if err != nil {
		return err
	}
	rec, err := s.store.Load(ctx, phone)
	if err != nil {
		return err
	}
	if shiftStartIndex(rec.Starts, hhmm) >= 0 {
		return ErrDuplicateShiftStart
	}
	rec.Starts = append(rec.Starts, SavedShiftStart{Time: hhmm, SetAt: s.now()})
	syncLegacyShiftStart(&rec)
	return s.store.SaveShiftStart(ctx, phone, rec)
}

// ReplaceShiftStart changes one saved time. The new time applies only to later scans.
func (s *PunctualityService) ReplaceShiftStart(ctx context.Context, phone, from, to string) error {
	from, err := normalizeShiftStart(from)
	if err != nil {
		return err
	}
	to, err = normalizeShiftStart(to)
	if err != nil {
		return err
	}
	rec, err := s.store.Load(ctx, phone)
	if err != nil {
		return err
	}
	idx := shiftStartIndex(rec.Starts, from)
	if idx < 0 {
		return ErrShiftStartNotFound
	}
	if from != to && shiftStartIndex(rec.Starts, to) >= 0 {
		return ErrDuplicateShiftStart
	}
	rec.Starts[idx] = SavedShiftStart{Time: to, SetAt: s.now()}
	syncLegacyShiftStart(&rec)
	return s.store.SaveShiftStart(ctx, phone, rec)
}

// RemoveShiftStart drops one time. The last remaining time cannot be removed.
func (s *PunctualityService) RemoveShiftStart(ctx context.Context, phone, hhmm string) error {
	hhmm, err := normalizeShiftStart(hhmm)
	if err != nil {
		return err
	}
	rec, err := s.store.Load(ctx, phone)
	if err != nil {
		return err
	}
	if len(rec.Starts) <= 1 {
		return ErrLastShiftStart
	}
	idx := shiftStartIndex(rec.Starts, hhmm)
	if idx < 0 {
		return ErrShiftStartNotFound
	}
	rec.Starts = append(rec.Starts[:idx], rec.Starts[idx+1:]...)
	syncLegacyShiftStart(&rec)
	return s.store.SaveShiftStart(ctx, phone, rec)
}

func normalizeShiftStart(hhmm string) (string, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(hhmm))
	if err != nil {
		return "", fmt.Errorf("shift start must be HH:MM: %w", err)
	}
	return parsed.Format("15:04"), nil
}

func shiftStartIndex(starts []SavedShiftStart, hhmm string) int {
	for i, start := range starts {
		if start.Time == hhmm {
			return i
		}
	}
	return -1
}

func syncLegacyShiftStart(rec *PunctualityRecord) {
	if len(rec.Starts) == 0 {
		rec.ShiftStart = ""
		rec.ShiftStartSetAt = time.Time{}
		return
	}
	sorted := append([]SavedShiftStart(nil), rec.Starts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time < sorted[j].Time })
	rec.Starts = sorted
	rec.ShiftStart = sorted[0].Time
	rec.ShiftStartSetAt = sorted[0].SetAt
}

// RecordScan scores the first scan_start of a Zambia day when a shift start
// was already saved. Later scans and scan_return leave the record unchanged.
func (s *PunctualityService) RecordScan(ctx context.Context, phone, reason string, scanAt time.Time) error {
	rec, err := s.store.Load(ctx, phone)
	if err != nil {
		return err
	}
	localDate := scanAt.In(timezone.ZambiaLocation()).Format("2006-01-02")
	starts := make([]punctuality.ShiftStart, 0, len(rec.Starts))
	for _, start := range rec.Starts {
		starts = append(starts, punctuality.ShiftStart{Time: start.Time, SetAt: start.SetAt})
	}
	scored, ok := punctuality.ScoreScan(punctuality.ScoreInput{
		Starts:          starts,
		ShiftStart:      rec.ShiftStart,
		ShiftStartSetAt: rec.ShiftStartSetAt,
		ScanAt:          scanAt,
		Reason:          reason,
		AlreadyScored:   dayExists(rec.Days, localDate),
	})
	if !ok {
		return nil
	}
	rec.Days = append(rec.Days, PunctualityDay{
		Date:          scored.Date,
		ShiftStart:    scored.ShiftStart,
		FirstScan:     scored.FirstScan,
		SignedMinutes: scored.SignedMinutes,
		ScoredAt:      scanAt,
	})
	rec.OpenMinutes += scored.SignedMinutes
	if rec.LastScoredDate == "" || scored.Date >= rec.LastScoredDate {
		rec.LastScoredDate = scored.Date
		rec.LastScoredMinutes = scored.SignedMinutes
	}
	return s.store.SaveScore(ctx, phone, rec)
}

func dayExists(days []PunctualityDay, date string) bool {
	for _, day := range days {
		if day.Date == date {
			return true
		}
	}
	return false
}

func scoredDays(days []PunctualityDay) []punctuality.ScoredDay {
	out := make([]punctuality.ScoredDay, len(days))
	for i, day := range days {
		out[i] = punctuality.ScoredDay{ScoredAt: day.ScoredAt, SignedMinutes: day.SignedMinutes}
	}
	return out
}

func latestClear(settlements []PunctualitySettlement) time.Time {
	var latest time.Time
	for _, settlement := range settlements {
		if settlement.At.After(latest) {
			latest = settlement.At
		}
	}
	return latest
}

// Clear settles the open aggregate. It does not move money.
func (s *PunctualityService) Clear(ctx context.Context, phone, note, admin string) error {
	rec, err := s.store.Load(ctx, phone)
	if err != nil {
		return err
	}
	total := punctuality.OpenTotal(scoredDays(rec.Days), latestClear(rec.Settlements))
	if err := punctuality.ValidateClear(total, note); err != nil {
		return err
	}
	rec.Settlements = append(rec.Settlements, PunctualitySettlement{
		At:    s.now(),
		Total: total,
		Note:  strings.TrimSpace(note),
		Admin: strings.TrimSpace(admin),
	})
	rec.OpenMinutes = 0
	return s.store.SaveClear(ctx, phone, rec)
}

// PunctualityReport is the admin view of one day plus the open aggregate.
type PunctualityReport struct {
	ShiftStart  string                      `json:"shift_start"`
	ShiftStarts []string                    `json:"shift_starts"`
	Date        string                      `json:"date"`
	OpenMinutes int                         `json:"open_minutes"`
	Day         *PunctualityDayView         `json:"day"`
	Settlements []PunctualitySettlementView `json:"settlements"`
}

// PunctualityDayView is one scored day. Settled is true when a later clear covers it.
type PunctualityDayView struct {
	Date          string `json:"date"`
	ShiftStart    string `json:"shift_start"`
	FirstScan     string `json:"first_scan"`
	SignedMinutes int    `json:"signed_minutes"`
	Settled       bool   `json:"settled"`
}

// PunctualitySettlementView is one past clear.
type PunctualitySettlementView struct {
	At    string `json:"at"`
	Total int    `json:"total"`
	Note  string `json:"note"`
	Admin string `json:"admin"`
}

// Report returns the shift start, the selected Zambia day, the open aggregate,
// and settlement history. The open aggregate does not change with date.
func (s *PunctualityService) Report(ctx context.Context, phone, date string) (*PunctualityReport, error) {
	rec, err := s.store.Load(ctx, phone)
	if err != nil {
		return nil, err
	}
	if date == "" {
		date = s.now().In(timezone.ZambiaLocation()).Format("2006-01-02")
	}
	clearedAt := latestClear(rec.Settlements)
	starts := make([]string, 0, len(rec.Starts))
	for _, start := range rec.Starts {
		starts = append(starts, start.Time)
	}
	if len(starts) == 0 && rec.ShiftStart != "" {
		starts = []string{rec.ShiftStart}
	}
	report := &PunctualityReport{
		ShiftStart:  rec.ShiftStart,
		ShiftStarts: starts,
		Date:        date,
		OpenMinutes: punctuality.OpenTotal(scoredDays(rec.Days), clearedAt),
		Settlements: make([]PunctualitySettlementView, 0, len(rec.Settlements)),
	}
	for _, day := range rec.Days {
		if day.Date != date {
			continue
		}
		report.Day = &PunctualityDayView{
			Date:          day.Date,
			ShiftStart:    day.ShiftStart,
			FirstScan:     day.FirstScan,
			SignedMinutes: day.SignedMinutes,
			Settled:       !clearedAt.IsZero() && !day.ScoredAt.After(clearedAt),
		}
		break
	}
	// Newest clear first.
	for i := len(rec.Settlements) - 1; i >= 0; i-- {
		settlement := rec.Settlements[i]
		report.Settlements = append(report.Settlements, PunctualitySettlementView{
			At:    settlement.At.In(timezone.ZambiaLocation()).Format(time.RFC3339),
			Total: settlement.Total,
			Note:  settlement.Note,
			Admin: settlement.Admin,
		})
	}
	return report, nil
}
