package ids

import (
	"context"
	"errors"
	"regexp"
	"testing"
)

func TestFormatKnownVectors(t *testing.T) {
	cases := map[int64]string{1: "TR0458047115", 2: "TR2033899500"}
	for n, want := range cases {
		got, err := Trip.Format(n)
		if err != nil {
			t.Fatalf("Format(%d) error: %v", n, err)
		}
		if got != want {
			t.Fatalf("Trip.Format(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatShape(t *testing.T) {
	re := regexp.MustCompile(`^[A-Z]{2}\d{10}$`)
	for _, et := range allTypes() {
		got, err := et.Format(12345)
		if err != nil {
			t.Fatalf("%s Format error: %v", et.Prefix, err)
		}
		if !re.MatchString(got) {
			t.Fatalf("%s Format(12345) = %q, not 2-alpha + 10-digit", et.Prefix, got)
		}
	}
}

func TestFormatRange(t *testing.T) {
	for _, n := range []int64{0, -1, maxID + 1} {
		if _, err := Trip.Format(n); err == nil {
			t.Fatalf("Format(%d) expected error, got nil", n)
		}
	}
	if _, err := Trip.Format(maxID); err != nil {
		t.Fatalf("Format(maxID) unexpected error: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	ns := []int64{1, 2, 3, 100, 999999, 1000000000, maxID - 1, maxID}
	for _, n := range ns {
		id, err := Trip.Format(n)
		if err != nil {
			t.Fatalf("Format(%d): %v", n, err)
		}
		back, err := Trip.Decode(id)
		if err != nil {
			t.Fatalf("Decode(%q): %v", id, err)
		}
		if back != n {
			t.Fatalf("round-trip n=%d -> %q -> %d", n, id, back)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	bad := []string{"", "TR", "TRABCDEFGHIJ", "TR123", "XX0458047115", "TR04580471150"}
	for _, s := range bad {
		if _, err := Trip.Decode(s); err == nil {
			t.Fatalf("Decode(%q) expected error", s)
		}
	}
	// Cross-prefix: a Trip ID must not decode as a User ID.
	tripID, _ := Trip.Format(5)
	if _, err := User.Decode(tripID); err == nil {
		t.Fatalf("User.Decode(%q) should fail (wrong prefix)", tripID)
	}
}

func TestPrefixesAndKeysUnique(t *testing.T) {
	seenP, seenK := map[string]bool{}, map[string]bool{}
	for _, et := range allTypes() {
		if len(et.Prefix) != 2 {
			t.Fatalf("prefix %q not 2 chars", et.Prefix)
		}
		if seenP[et.Prefix] {
			t.Fatalf("duplicate prefix %q", et.Prefix)
		}
		if seenK[et.CounterKey] {
			t.Fatalf("duplicate counter key %q", et.CounterKey)
		}
		seenP[et.Prefix], seenK[et.CounterKey] = true, true
	}
}

type fakeCounter struct {
	n      int64
	err    error
	gotKey string
}

func (f *fakeCounter) NextValue(_ context.Context, counterKey string) (int64, error) {
	f.gotKey = counterKey
	if f.err != nil {
		return 0, f.err
	}
	f.n++
	return f.n, nil
}

func TestGeneratorNextID(t *testing.T) {
	g := NewGeneratorWithCounter(&fakeCounter{})
	first, err := g.NextID(context.Background(), Trip)
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if first != "TR0458047115" {
		t.Fatalf("first NextID = %q, want TR0458047115", first)
	}
	second, err := g.NextID(context.Background(), Trip)
	if err != nil {
		t.Fatalf("second NextID: %v", err)
	}
	if second != "TR2033899500" {
		t.Fatalf("second NextID = %q, want TR2033899500", second)
	}
}

func TestGeneratorPropagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	g := NewGeneratorWithCounter(&fakeCounter{err: sentinel})
	_, err := g.NextID(context.Background(), Trip)
	if err == nil {
		t.Fatalf("expected error from counter")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("NextID error = %v, want sentinel %v", err, sentinel)
	}
}

func TestGeneratorRoutesCounterKey(t *testing.T) {
	ctx := context.Background()
	fc := &fakeCounter{}
	g := NewGeneratorWithCounter(fc)

	if _, err := g.NextID(ctx, Trip); err != nil {
		t.Fatalf("NextID(Trip): %v", err)
	}
	if fc.gotKey != "COUNTER!TRIP" {
		t.Fatalf("Trip counter key = %q, want COUNTER!TRIP", fc.gotKey)
	}

	if _, err := g.NextID(ctx, User); err != nil {
		t.Fatalf("NextID(User): %v", err)
	}
	if fc.gotKey != "COUNTER!USER" {
		t.Fatalf("User counter key = %q, want COUNTER!USER", fc.gotKey)
	}
}

func allTypes() []EntityType {
	return []EntityType{User, DE, Trip, Task, Address, Dispute, Earning, Disbursement, CashDeposit}
}
