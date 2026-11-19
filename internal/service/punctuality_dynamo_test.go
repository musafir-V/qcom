package service

import (
	"context"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/repository"
)

type fakePunctualityRepo struct {
	day   repository.PunctualityStoredDay
	open  int
	last  string
	mins  int
	calls int
}

func (f *fakePunctualityRepo) Load(context.Context, string) (repository.PunctualityState, error) {
	return repository.PunctualityState{}, nil
}
func (f *fakePunctualityRepo) UpdateShiftStarts(context.Context, string, []repository.PunctualityStoredStart) error {
	return nil
}
func (f *fakePunctualityRepo) PutDay(_ context.Context, _ string, day repository.PunctualityStoredDay, open int, lastDate string, lastMinutes int) error {
	f.calls++
	f.day = day
	f.open = open
	f.last = lastDate
	f.mins = lastMinutes
	return nil
}
func (f *fakePunctualityRepo) PutClear(context.Context, string, repository.PunctualityStoredClear) error {
	return nil
}

func TestDynamoStoreSaveScore_WritesOnlyTheNewDay(t *testing.T) {
	repo := &fakePunctualityRepo{}
	store := dynamoPunctualityStore{repo: repo}
	scoredAt := time.Date(2026, 6, 22, 6, 20, 0, 0, time.UTC)
	err := store.SaveScore(context.Background(), "+260971000001", PunctualityRecord{
		OpenMinutes:       -10,
		LastScoredDate:    "2026-06-22",
		LastScoredMinutes: -20,
		Days: []PunctualityDay{
			{Date: "2026-06-21", SignedMinutes: 10, ScoredAt: scoredAt.Add(-24 * time.Hour)},
			{Date: "2026-06-22", ShiftStart: "08:00", FirstScan: "08:20", SignedMinutes: -20, ScoredAt: scoredAt},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.calls != 1 || repo.day.Date != "2026-06-22" || repo.day.SignedMinutes != -20 || repo.open != -10 || repo.last != "2026-06-22" || repo.mins != -20 {
		t.Fatalf("put = %+v calls=%d open=%d last=%s mins=%d", repo.day, repo.calls, repo.open, repo.last, repo.mins)
	}
}
