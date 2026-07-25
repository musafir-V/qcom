package service

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
)

func TestSortTripsByCreatedAt_OldestFirst(t *testing.T) {
	trips := []*models.Trip{
		{TripID: "c", CreatedAt: "2026-06-02T10:00:02Z"},
		{TripID: "a", CreatedAt: "2026-06-02T10:00:00Z"},
		{TripID: "b", CreatedAt: "2026-06-02T10:00:01Z"},
	}

	sortTripsByCreatedAt(trips)

	got := []string{trips[0].TripID, trips[1].TripID, trips[2].TripID}
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FIFO order wrong: got %v, want %v", got, want)
		}
	}
}

func TestSortTripsByCreatedAt_StableForEqualTimestamps(t *testing.T) {
	trips := []*models.Trip{
		{TripID: "first", CreatedAt: "2026-06-02T10:00:00Z"},
		{TripID: "second", CreatedAt: "2026-06-02T10:00:00Z"},
	}

	sortTripsByCreatedAt(trips)

	if trips[0].TripID != "first" || trips[1].TripID != "second" {
		t.Fatalf("equal timestamps should preserve input order, got %s,%s", trips[0].TripID, trips[1].TripID)
	}
}

func TestIsAcceptExpired(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	expired := &models.Trip{
		Status:         models.TripStatusAssigned,
		AcceptDeadline: now.Add(-1 * time.Second).Format(time.RFC3339),
	}
	if !isAcceptExpired(expired, now) {
		t.Error("expected expired assigned trip to be auto-rejectable")
	}

	future := &models.Trip{
		Status:         models.TripStatusAssigned,
		AcceptDeadline: now.Add(30 * time.Second).Format(time.RFC3339),
	}
	if isAcceptExpired(future, now) {
		t.Error("expected future-deadline trip to NOT be expired")
	}

	accepted := &models.Trip{
		Status:         models.TripStatusAccepted,
		AcceptDeadline: now.Add(-1 * time.Second).Format(time.RFC3339),
	}
	if isAcceptExpired(accepted, now) {
		t.Error("expected accepted trip to never be auto-rejected")
	}

	noDeadline := &models.Trip{Status: models.TripStatusAssigned}
	if isAcceptExpired(noDeadline, now) {
		t.Error("expected trip with no deadline to NOT be expired")
	}
}

func TestTripItemsFromOrder_MapsFields(t *testing.T) {
	order := JavaOrder{
		Items: []JavaOrderItem{
			{ProductName: "Milk", ImageURL: "items/milk.png", FulfilledQuantity: intPtr(2), OrderedQuantity: intPtr(3), Sku: "SKU-1"},
			{ProductName: "Bread", ImageURL: "items/bread.png", FulfilledQuantity: intPtr(1), OrderedQuantity: intPtr(1), Sku: "SKU-2"},
		},
	}

	items := tripItemsFromOrder(order)

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0] != (models.TripItem{Name: "Milk", ImageURL: "items/milk.png", Quantity: 2, Sku: "SKU-1"}) {
		t.Errorf("item[0] mismatch: got %+v", items[0])
	}
	if items[1] != (models.TripItem{Name: "Bread", ImageURL: "items/bread.png", Quantity: 1, Sku: "SKU-2"}) {
		t.Errorf("item[1] mismatch: got %+v", items[1])
	}
}

func intPtr(v int) *int { return &v }

func TestTripItemsFromOrder_EmptyIsNil(t *testing.T) {
	if items := tripItemsFromOrder(JavaOrder{}); items != nil {
		t.Errorf("expected nil items for order with no items, got %+v", items)
	}
}

func TestFirstNonEmpty_RecipientName(t *testing.T) {
	// Name present -> use delivery.name.
	if got := firstNonEmpty("Jane Doe", recipientFallback); got != "Jane Doe" {
		t.Errorf("expected delivery name, got %q", got)
	}
	// Name empty -> fall back.
	if got := firstNonEmpty("", recipientFallback); got != recipientFallback {
		t.Errorf("expected fallback %q, got %q", recipientFallback, got)
	}
	// Name is only whitespace -> fall back.
	if got := firstNonEmpty("   ", recipientFallback); got != recipientFallback {
		t.Errorf("expected fallback %q for blank name, got %q", recipientFallback, got)
	}
}

func TestRandomOTP_FourNumericDigitsInRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		otp := randomOTP()
		if len(otp) != 4 {
			t.Fatalf("otp %q is not 4 characters", otp)
		}
		n, err := strconv.Atoi(otp)
		if err != nil {
			t.Fatalf("otp %q is not numeric: %v", otp, err)
		}
		if n < 1000 || n > 9999 {
			t.Fatalf("otp %d out of range [1000,9999]", n)
		}
	}
}

func TestPaymentFromOrder_COD_CollectsCash(t *testing.T) {
	order := JavaOrder{PaymentMethod: "COD", GrandTotal: 12.74, Currency: "ZMW"}
	p := paymentFromOrder(order)
	if !p.CollectCash {
		t.Error("expected CollectCash==true for COD")
	}
	if p.AmountZMW != 12.74 {
		t.Errorf("expected AmountZMW==12.74, got %v", p.AmountZMW)
	}
	if p.Currency != "ZMW" {
		t.Errorf("expected Currency==ZMW, got %q", p.Currency)
	}
}

func TestPaymentFromOrder_Online_NoCollect(t *testing.T) {
	order := JavaOrder{PaymentMethod: "AIRTEL_MONEY", GrandTotal: 50}
	p := paymentFromOrder(order)
	if p.CollectCash {
		t.Error("expected CollectCash==false for AIRTEL_MONEY")
	}
	if p.AmountZMW != 50 {
		t.Errorf("expected AmountZMW==50, got %v", p.AmountZMW)
	}
}

func TestPaymentFromOrder_UnknownMethodTreatedAsOnline(t *testing.T) {
	order := JavaOrder{PaymentMethod: "WEIRD_NEW_METHOD"}
	p := paymentFromOrder(order)
	if p.CollectCash {
		t.Error("expected CollectCash==false for unrecognized method")
	}
}

func TestPaymentFromOrder_EmptyMethod(t *testing.T) {
	p := paymentFromOrder(JavaOrder{})
	if p.CollectCash {
		t.Error("expected CollectCash==false for empty order")
	}
}

func TestIsKnownPaymentMethod(t *testing.T) {
	known := []string{"COD", "AIRTEL_MONEY", "CARD", "BANK_TRANSFER"}
	for _, m := range known {
		if !isKnownPaymentMethod(m) {
			t.Errorf("expected %q to be a known payment method", m)
		}
	}
	unknown := []string{"", "WEIRD"}
	for _, m := range unknown {
		if isKnownPaymentMethod(m) {
			t.Errorf("expected %q to be unrecognized", m)
		}
	}
}

func TestBuildTripCarriesCustomerUserID(t *testing.T) {
	order := JavaOrder{
		OrderID:    "ORD1",
		CustomerID: "U9",
		Delivery:   JavaDelivery{Phone: "+260970000000", Name: "Ada"},
	}
	ds := &models.Darkstore{Name: "Store A", Latitude: -15.4, Longitude: 28.3}
	trip := buildTripFromOrder(order, "trip-1", "pick-1", "drop-1", "ORD1", "221", 3.5, 12.0, ds)
	if trip.CustomerUserID != "U9" {
		t.Fatalf("CustomerUserID = %q, want U9", trip.CustomerUserID)
	}
}

func TestStampAssignmentDecision_DefaultDecisionAndSLA(t *testing.T) {
	trip := &models.Trip{DistanceKM: 5.0}
	cfg := &models.PayoutConfig{RatePerKmZMW: 2.0, MinutesPerKm: 4.0}
	assignedAt := time.Date(2026, 6, 22, 10, 0, 0, 0, mustLoadLusaka(t))

	stampAssignmentDecision(trip, cfg, assignedAt, nil)

	if trip.SLAMinutes != 20 {
		t.Fatalf("expected SLA 20, got %.2f", trip.SLAMinutes)
	}
	if trip.RateMultiplier != 1 || trip.RateFlatZMW != 0 {
		t.Fatalf("expected default decision, got multiplier=%.2f flat=%.2f", trip.RateMultiplier, trip.RateFlatZMW)
	}
	if trip.RateRuleID != "" || trip.RateRuleVersion != 0 {
		t.Fatalf("expected no winning rule metadata, got id=%q version=%d", trip.RateRuleID, trip.RateRuleVersion)
	}
}

func TestStampAssignmentDecision_ResolvesRuleAtAssignmentTime(t *testing.T) {
	loc := mustLoadLusaka(t)
	assignedAt := time.Date(2026, 6, 22, 18, 0, 0, 0, loc)
	spec, err := json.Marshal(models.RateModifierSpec{
		DaysOfWeek: []int{int(time.Monday)},
		StartTime:  "17:00",
		EndTime:    "22:00",
		Multiplier: 1.3,
	})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	engine := newTestFareEngine(t, []*models.Rule{
		{ID: "evening-surge", Family: models.FamilyRateModifier, Enabled: true, Priority: 1, Version: 3, Spec: spec},
	})

	trip := &models.Trip{DistanceKM: 5.0}
	cfg := &models.PayoutConfig{RatePerKmZMW: 2.0, MinutesPerKm: 4.0}
	stampAssignmentDecision(trip, cfg, assignedAt, engine)

	if trip.RateRuleID != "evening-surge" || trip.RateRuleVersion != 3 {
		t.Fatalf("unexpected winner metadata: id=%q version=%d", trip.RateRuleID, trip.RateRuleVersion)
	}
	if trip.RateMultiplier != 1.3 || trip.RateFlatZMW != 0 {
		t.Fatalf("unexpected rate decision: multiplier=%.2f flat=%.2f", trip.RateMultiplier, trip.RateFlatZMW)
	}
}
