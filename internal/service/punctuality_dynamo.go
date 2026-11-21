package service

import (
	"context"

	"github.com/qcom/qcom/internal/repository"
)

// punctualityPersistence is the Dynamo surface for arrival scores.
type punctualityPersistence interface {
	Load(ctx context.Context, phone string) (repository.PunctualityState, error)
	UpdateShiftStarts(ctx context.Context, phone string, starts []repository.PunctualityStoredStart) error
	PutDay(ctx context.Context, phone string, day repository.PunctualityStoredDay, openMinutes int, lastDate string, lastMinutes int) error
	PutClear(ctx context.Context, phone string, clear repository.PunctualityStoredClear) error
}

type dynamoPunctualityStore struct {
	repo punctualityPersistence
}

// NewDynamoPunctualityStore adapts the Dynamo repository to PunctualityStore.
func NewDynamoPunctualityStore(repo *repository.PunctualityRepository) PunctualityStore {
	return dynamoPunctualityStore{repo: repo}
}

func (s dynamoPunctualityStore) Load(ctx context.Context, phone string) (PunctualityRecord, error) {
	state, err := s.repo.Load(ctx, phone)
	if err != nil {
		return PunctualityRecord{}, err
	}
	rec := PunctualityRecord{
		ShiftStart:        state.ShiftStart,
		ShiftStartSetAt:   state.ShiftStartSetAt,
		Starts:            make([]SavedShiftStart, 0, len(state.Starts)),
		OpenMinutes:       state.OpenMinutes,
		LastScoredDate:    state.LastScoredDate,
		LastScoredMinutes: state.LastScoredMinutes,
		Days:              make([]PunctualityDay, 0, len(state.Days)),
		Settlements:       make([]PunctualitySettlement, 0, len(state.Settlements)),
	}
	for _, start := range state.Starts {
		rec.Starts = append(rec.Starts, SavedShiftStart{Time: start.Time, SetAt: start.SetAt})
	}
	for _, day := range state.Days {
		rec.Days = append(rec.Days, PunctualityDay{
			Date: day.Date, ShiftStart: day.ShiftStart, FirstScan: day.FirstScan,
			SignedMinutes: day.SignedMinutes, ScoredAt: day.ScoredAt,
		})
	}
	for _, clear := range state.Settlements {
		rec.Settlements = append(rec.Settlements, PunctualitySettlement{
			At: clear.At, Total: clear.Total, Note: clear.Note, Admin: clear.Admin, Partial: clear.Partial,
		})
	}
	return rec, nil
}

func (s dynamoPunctualityStore) SaveShiftStart(ctx context.Context, phone string, rec PunctualityRecord) error {
	starts := make([]repository.PunctualityStoredStart, len(rec.Starts))
	for i, start := range rec.Starts {
		starts[i] = repository.PunctualityStoredStart{Time: start.Time, SetAt: start.SetAt}
	}
	return s.repo.UpdateShiftStarts(ctx, phone, starts)
}

func (s dynamoPunctualityStore) SaveScore(ctx context.Context, phone string, rec PunctualityRecord) error {
	if len(rec.Days) == 0 {
		return nil
	}
	day := rec.Days[len(rec.Days)-1]
	return s.repo.PutDay(ctx, phone, repository.PunctualityStoredDay{
		Date: day.Date, ShiftStart: day.ShiftStart, FirstScan: day.FirstScan,
		SignedMinutes: day.SignedMinutes, ScoredAt: day.ScoredAt,
	}, rec.OpenMinutes, rec.LastScoredDate, rec.LastScoredMinutes)
}

func (s dynamoPunctualityStore) SaveClear(ctx context.Context, phone string, rec PunctualityRecord) error {
	if len(rec.Settlements) == 0 {
		return nil
	}
	clear := rec.Settlements[len(rec.Settlements)-1]
	return s.repo.PutClear(ctx, phone, repository.PunctualityStoredClear{
		At: clear.At, Total: clear.Total, Note: clear.Note, Admin: clear.Admin,
		Partial: clear.Partial, OpenMinutes: rec.OpenMinutes,
	})
}
