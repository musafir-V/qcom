package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type stubRewardDERepo struct {
	des []*models.DeliveryExecutive
	err error
}

func (s *stubRewardDERepo) ScanAll(_ context.Context) ([]*models.DeliveryExecutive, error) {
	return s.des, s.err
}

type stubRewardTripRepo struct {
	perDE map[string][]*models.Trip
	err   error
}

// ListByDEWindow mirrors the production repository's BETWEEN semantics so the
// tests actually exercise window membership: a trip is in-window if its
// completed_at OR cancelled_at falls lexicographically within [start, end].
func (s *stubRewardTripRepo) ListByDEWindow(_ context.Context, deID, start, end string, _ int32, _ map[string]types.AttributeValue) ([]*models.Trip, map[string]types.AttributeValue, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	var out []*models.Trip
	for _, trip := range s.perDE[deID] {
		if betweenStr(trip.CompletedAt, start, end) || betweenStr(trip.CancelledAt, start, end) {
			out = append(out, trip)
		}
	}
	return out, nil, nil
}

func betweenStr(ts, start, end string) bool {
	return ts != "" && ts >= start && ts <= end
}

type stubRewardRuleRepo struct {
	rules []*models.Rule
	err   error
}

func (s *stubRewardRuleRepo) ListAll(_ context.Context) ([]*models.Rule, error) {
	return s.rules, s.err
}

type stubRewardLedgerRepo struct {
	exists map[string]bool
	app    []*models.EarningsLedger
}

func (s *stubRewardLedgerRepo) ExistsByReference(_ context.Context, deID string, earningType models.EarningType, referenceID string) (bool, error) {
	return s.exists[deID+"|"+string(earningType)+"|"+referenceID], nil
}

func (s *stubRewardLedgerRepo) Append(_ context.Context, entry *models.EarningsLedger) error {
	s.app = append(s.app, entry)
	return nil
}

type stubRewardLockRepo struct{}

func (s *stubRewardLockRepo) AcquireWithSK(_ context.Context, _ string, _ int) (bool, error) {
	return true, nil
}

func (s *stubRewardLockRepo) ReleaseWithSK(_ context.Context, _ string) error {
	return nil
}

func TestRewardCron_RunDailyWindow_EmitsAndIsIdempotent(t *testing.T) {
	specBytes, err := json.Marshal(models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "daily",
		Threshold:     10,
		RequireNoFail: true,
		Reward: models.Reward{
			Kind:      "cash",
			AmountZMW: 25,
		},
	})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	rule := &models.Rule{
		ID:      "b1_daily_bonus",
		Family:  models.FamilyAccumulator,
		Enabled: true,
		Version: 1,
		Spec:    specBytes,
	}
	windowDay := time.Date(2026, 6, 22, 0, 0, 0, 0, mustLoadLusaka(t))
	windowKey := "2026-06-22"
	repo := &stubRewardLedgerRepo{exists: map[string]bool{}}
	cron := newRewardCronWithDeps(
		&stubRewardDERepo{des: []*models.DeliveryExecutive{{DEID: "de-1"}}},
		&stubRewardTripRepo{perDE: map[string][]*models.Trip{"de-1": makeOnTimeTrips(10, "2026-06-22T08:00:00+02:00")}},
		&stubRewardRuleRepo{rules: []*models.Rule{rule}},
		repo,
		&stubRewardLockRepo{},
		logrus.New(),
	)

	cron.runDailyWindow(context.Background(), windowDay)
	if len(repo.app) != 1 {
		t.Fatalf("appended entries = %d, want 1", len(repo.app))
	}
	if repo.app[0].ReferenceID != windowKey {
		t.Fatalf("reference_id = %q, want %q", repo.app[0].ReferenceID, windowKey)
	}

	repo.exists["de-1|"+string(models.EarningTypeB1DailyBonus)+"|"+windowKey] = true
	cron.runDailyWindow(context.Background(), windowDay)
	if len(repo.app) != 1 {
		t.Fatalf("second run should be idempotent; appended entries = %d, want 1", len(repo.app))
	}
}

func TestRewardCron_RunDailyWindow_RespectsWindowBounds(t *testing.T) {
	specBytes, err := json.Marshal(models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "daily",
		Threshold:     10,
		RequireNoFail: true,
		Reward: models.Reward{
			Kind:      "cash",
			AmountZMW: 25,
		},
	})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	rule := &models.Rule{
		ID:      "b1_daily_bonus",
		Family:  models.FamilyAccumulator,
		Enabled: true,
		Version: 1,
		Spec:    specBytes,
	}
	windowDay := time.Date(2026, 6, 22, 0, 0, 0, 0, mustLoadLusaka(t))

	// Trips are stored in UTC (matching trip_repository.go). The Zambia day
	// 2026-06-22 maps to the UTC window [2026-06-21T22:00:00Z, 2026-06-22T21:59:59Z].
	insideTrips := func() []*models.Trip {
		out := make([]*models.Trip, 0, 10)
		for i := 0; i < 10; i++ {
			out = append(out, completedTrip(true, "2026-06-22T12:00:00Z")) // mid Zambia day
		}
		return out
	}

	t.Run("all_inside_window_awards", func(t *testing.T) {
		repo := &stubRewardLedgerRepo{exists: map[string]bool{}}
		cron := newRewardCronWithDeps(
			&stubRewardDERepo{des: []*models.DeliveryExecutive{{DEID: "de-1"}}},
			&stubRewardTripRepo{perDE: map[string][]*models.Trip{"de-1": insideTrips()}},
			&stubRewardRuleRepo{rules: []*models.Rule{rule}},
			repo,
			&stubRewardLockRepo{},
			logrus.New(),
		)
		cron.runDailyWindow(context.Background(), windowDay)
		if len(repo.app) != 1 {
			t.Fatalf("appended entries = %d, want 1", len(repo.app))
		}
	})

	t.Run("out_of_window_trips_excluded", func(t *testing.T) {
		// 9 trips inside the window plus 5 on-time trips at 2026-06-22T22:30:00Z.
		// In UTC that is 00:30 +02:00 on 2026-06-23 — the NEXT Zambia day — so
		// they are past the 2026-06-22T21:59:59Z end bound and must be excluded.
		// This is the discriminating case for the timezone fix: with the old
		// Zambia-local bounds (...T23:59:59+02:00) these trips compared as
		// in-window (string "22:30:00Z" < "23:59:59+02:00"), wrongly pushing the
		// count to 14 and emitting a reward. With UTC bounds only 9 count, so the
		// threshold of 10 is not met and nothing is emitted.
		trips := insideTrips()[:9]
		for i := 0; i < 5; i++ {
			trips = append(trips, completedTrip(true, "2026-06-22T22:30:00Z"))
		}
		repo := &stubRewardLedgerRepo{exists: map[string]bool{}}
		cron := newRewardCronWithDeps(
			&stubRewardDERepo{des: []*models.DeliveryExecutive{{DEID: "de-1"}}},
			&stubRewardTripRepo{perDE: map[string][]*models.Trip{"de-1": trips}},
			&stubRewardRuleRepo{rules: []*models.Rule{rule}},
			repo,
			&stubRewardLockRepo{},
			logrus.New(),
		)
		cron.runDailyWindow(context.Background(), windowDay)
		if len(repo.app) != 0 {
			t.Fatalf("appended entries = %d, want 0 (out-of-window trips must not count)", len(repo.app))
		}
	})
}

func TestRewardCron_RunWeeklyWindow_EmitsAccumulatorAndRanking(t *testing.T) {
	b2Spec, _ := json.Marshal(models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "weekly",
		Threshold:     80,
		MinOnTimeRate: 0.95,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Mealie Bag",
			SKU:       "mealie_bag",
		},
	})
	b3Spec, _ := json.Marshal(models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "weekly",
		Threshold:     100,
		MinOnTimeRate: 0.95,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Household Item",
			SKU:       "household_item",
		},
	})
	b4Spec, _ := json.Marshal(models.RankingSpec{
		Window:       "weekly",
		TopN:         1,
		MinOnTime:    1,
		WeightRate:   0.5,
		WeightVolume: 0.5,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Weekly Gift",
			SKU:       "weekly_gift",
		},
	})
	rules := []*models.Rule{
		{ID: "b2", Family: models.FamilyAccumulator, Enabled: true, Version: 1, Spec: b2Spec},
		{ID: "b3", Family: models.FamilyAccumulator, Enabled: true, Version: 1, Spec: b3Spec},
		{ID: "b4", Family: models.FamilyRanking, Enabled: true, Version: 1, Spec: b4Spec},
	}
	perDE := map[string][]*models.Trip{
		"de-1": append(makeOnTimeTrips(100, "2026-06-16T08:00:00+02:00"), completedTrip(false, "2026-06-16T14:00:00+02:00")),
		"de-2": makeOnTimeTrips(70, "2026-06-16T08:00:00+02:00"),
	}
	repo := &stubRewardLedgerRepo{exists: map[string]bool{}}
	cron := newRewardCronWithDeps(
		&stubRewardDERepo{des: []*models.DeliveryExecutive{{DEID: "de-1"}, {DEID: "de-2"}}},
		&stubRewardTripRepo{perDE: perDE},
		&stubRewardRuleRepo{rules: rules},
		repo,
		&stubRewardLockRepo{},
		logrus.New(),
	)

	weekStart := time.Date(2026, 6, 15, 0, 0, 0, 0, mustLoadLusaka(t))
	cron.runWeeklyWindow(context.Background(), weekStart)

	if len(repo.app) != 3 {
		t.Fatalf("appended entries = %d, want 3 (B2, B3, B4)", len(repo.app))
	}
}
