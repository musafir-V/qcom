package models

import (
	"math"
	"testing"
	"time"
)

func TestDropDeadlineConfigKeys(t *testing.T) {
	c := &DropDeadlineConfig{}
	if c.GetPK() != "CONFIG" || c.GetSK() != "DROP_DEADLINE_V1" {
		t.Fatalf("keys = %s/%s", c.GetPK(), c.GetSK())
	}
}

func TestEffectiveMinutesPerKm_DefaultWhenNilOrNonPositive(t *testing.T) {
	if (*DropDeadlineConfig)(nil).EffectiveMinutesPerKm() != 2 {
		t.Fatal("nil")
	}
	if (&DropDeadlineConfig{}).EffectiveMinutesPerKm() != 2 {
		t.Fatal("zero")
	}
	if (&DropDeadlineConfig{MinutesPerKm: -1}).EffectiveMinutesPerKm() != 2 {
		t.Fatal("neg")
	}
}

func TestEffectiveMinutesPerKm_Positive(t *testing.T) {
	if (&DropDeadlineConfig{MinutesPerKm: 3.5}).EffectiveMinutesPerKm() != 3.5 {
		t.Fatal()
	}
}

func TestEffectiveExtraMinutes_ZeroIsValid(t *testing.T) {
	if (*DropDeadlineConfig)(nil).EffectiveExtraMinutes() != 0 {
		t.Fatal("nil")
	}
	if (&DropDeadlineConfig{ExtraMinutes: 0}).EffectiveExtraMinutes() != 0 {
		t.Fatal("0")
	}
	if (&DropDeadlineConfig{ExtraMinutes: 5}).EffectiveExtraMinutes() != 5 {
		t.Fatal("5")
	}
	if (&DropDeadlineConfig{ExtraMinutes: -4}).EffectiveExtraMinutes() != 0 {
		t.Fatal("neg")
	}
}

func TestComputeDropDeadlineUnix_FormulaNoCeil(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	got := ComputeDropDeadlineUnix(now, 3.2, 2, 0) // 6.4 min = 384s, do not ceil to 7
	want := now.Add(6*time.Minute + 24*time.Second).Unix()
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

func TestComputeDropDeadlineUnix_UsesXAndY(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	got := ComputeDropDeadlineUnix(now, 2, 3, 4) // 10 min
	if got != now.Add(10*time.Minute).Unix() {
		t.Fatal()
	}
}

func TestComputeDropDeadlineUnix_NegativeMinutesClampedToZero(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	got := ComputeDropDeadlineUnix(now, 1, -10, 0)
	if got != now.Unix() {
		t.Fatalf("got %d, want %d", got, now.Unix())
	}
}

func TestComputeDropDeadlineUnix_MaxBoundDoesNotWrapToPast(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	got := ComputeDropDeadlineUnix(now, 0, 0, maxDropDeadlineMinutes)
	if got < now.Unix() {
		t.Fatalf("got %d before now %d at maxDropDeadlineMinutes (Duration wrapped)", got, now.Unix())
	}
}

func TestMinutesFitDropDeadlineDuration_RejectsMaxBound(t *testing.T) {
	if MinutesFitDropDeadlineDuration(maxDropDeadlineMinutes) {
		t.Fatal("max bound must be rejected so PATCH cannot persist a wrapping Duration")
	}
	if !MinutesFitDropDeadlineDuration(200) {
		t.Fatal("safe extra_minutes=200 must still fit")
	}
}

func TestComputeDropDeadlineUnix_OverflowDoesNotWrapToPast(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	got := ComputeDropDeadlineUnix(now, 0, 2, 200000000)
	if got < now.Unix() {
		t.Fatalf("got %d before now %d (duration wrapped to MinInt64)", got, now.Unix())
	}
	want := now.Add(time.Duration(math.MaxInt64)).Unix()
	if got != want {
		t.Fatalf("got %d, want clamped MaxInt64 duration unix %d", got, want)
	}
}

func TestComputeDropDeadlineUnix_NotCustomerETA(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	// Customer ETA would be ceil(0.5*2)+3 = 4 min. Driver timer is 0.5*2+0 = 1 min.
	got := ComputeDropDeadlineUnix(now, 0.5, 2, 0)
	if got != now.Add(time.Minute).Unix() {
		t.Fatal("must not be ETA+pack")
	}
}
