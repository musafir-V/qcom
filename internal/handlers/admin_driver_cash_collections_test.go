package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
)

// --- catWindowRFC3339 --------------------------------------------------

func TestCatWindowRFC3339(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		to      string
		wantErr error
		// wantStart/wantEnd are only checked when wantErr is nil.
		wantStart string
		wantEnd   string
	}{
		{
			name:      "single day",
			from:      "2026-01-15",
			to:        "2026-01-15",
			wantStart: "2026-01-14T22:00:00Z", // 2026-01-15 00:00:00 CAT (UTC+2) -> UTC
			wantEnd:   "2026-01-15T21:59:59Z", // 2026-01-15 23:59:59 CAT -> UTC
		},
		{
			name:      "multi day range",
			from:      "2026-01-01",
			to:        "2026-01-07",
			wantStart: "2025-12-31T22:00:00Z",
			wantEnd:   "2026-01-07T21:59:59Z",
		},
		{
			name:      "exactly 31 days is allowed",
			from:      "2026-01-01",
			to:        "2026-01-31",
			wantStart: "2025-12-31T22:00:00Z",
			wantEnd:   "2026-01-31T21:59:59Z",
		},
		{
			name:    "32 days is rejected",
			from:    "2026-01-01",
			to:      "2026-02-01",
			wantErr: errCashCollectionsRangeTooWide,
		},
		{
			name:    "to before from is rejected",
			from:    "2026-01-10",
			to:      "2026-01-01",
			wantErr: errCashCollectionsInvalidRange,
		},
		{
			name:    "bad from format",
			from:    "01-15-2026",
			to:      "2026-01-15",
			wantErr: errCashCollectionsInvalidDate,
		},
		{
			name:    "bad to format",
			from:    "2026-01-15",
			to:      "not-a-date",
			wantErr: errCashCollectionsInvalidDate,
		},
		{
			name:    "empty from",
			from:    "",
			to:      "2026-01-15",
			wantErr: errCashCollectionsInvalidDate,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, err := catWindowRFC3339(c.from, c.to)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("catWindowRFC3339(%q, %q) err = %v, want wrapping %v", c.from, c.to, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("catWindowRFC3339(%q, %q) unexpected err: %v", c.from, c.to, err)
			}
			if start != c.wantStart {
				t.Errorf("start = %q, want %q", start, c.wantStart)
			}
			if end != c.wantEnd {
				t.Errorf("end = %q, want %q", end, c.wantEnd)
			}
		})
	}
}

// --- cursor encode/decode -----------------------------------------------

func TestCashCollectionsCursorRoundTrip(t *testing.T) {
	cases := []int{0, 1, 50, 12345}
	for _, offset := range cases {
		encoded := encodeCashCollectionsCursor(offset)
		if offset == 0 && encoded != "" {
			t.Errorf("encodeCashCollectionsCursor(0) = %q, want empty string", encoded)
		}
		decoded, err := decodeCashCollectionsCursor(encoded)
		if err != nil {
			t.Fatalf("decodeCashCollectionsCursor(%q) unexpected err: %v", encoded, err)
		}
		if decoded != offset {
			t.Errorf("round trip offset = %d, want %d", decoded, offset)
		}
	}
}

func TestDecodeCashCollectionsCursor_Invalid(t *testing.T) {
	cases := []string{"not-base64!!!", "aGVsbG8=" /* "hello", not a number */}
	for _, cursor := range cases {
		if _, err := decodeCashCollectionsCursor(cursor); err == nil {
			t.Errorf("decodeCashCollectionsCursor(%q) expected error, got nil", cursor)
		}
	}
}

func TestDecodeCashCollectionsCursor_Empty(t *testing.T) {
	offset, err := decodeCashCollectionsCursor("")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if offset != 0 {
		t.Errorf("offset = %d, want 0", offset)
	}
}

// --- clampCashCollectionsLimit -------------------------------------------

func TestClampCashCollectionsLimit(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", cashCollectionsDefaultLimit},
		{"not-a-number", cashCollectionsDefaultLimit},
		{"0", cashCollectionsDefaultLimit},
		{"-5", cashCollectionsDefaultLimit},
		{"25", 25},
		{"100", 100},
		{"500", cashCollectionsMaxLimit},
	}
	for _, c := range cases {
		if got := clampCashCollectionsLimit(c.raw); got != c.want {
			t.Errorf("clampCashCollectionsLimit(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

// --- fetchCashCollections -------------------------------------------------

// fakeTripLister implements cashCollectionsTripLister in-memory, letting
// tests exercise pagination without a real DynamoDB client. Trips are
// returned newest-first (matching the real GSI's ScanIndexForward: false) in
// pages of at most the caller's pageSize.
type fakeTripLister struct {
	trips []*models.Trip
	err   error
	// pagesFetched counts how many times ListByDECompletedBetween was called,
	// so tests can assert multi-page draining actually happened.
	pagesFetched int
}

func (f *fakeTripLister) ListByDECompletedBetween(ctx context.Context, deID, startTS, endTS string, pageSize int32, lastKey map[string]types.AttributeValue) ([]*models.Trip, map[string]types.AttributeValue, error) {
	f.pagesFetched++
	if f.err != nil {
		return nil, nil, f.err
	}

	offset := 0
	if lastKey != nil {
		if v, ok := lastKey["offset"].(*types.AttributeValueMemberN); ok {
			offset, _ = parseIntOrZero(v.Value)
		}
	}

	end := offset + int(pageSize)
	if end > len(f.trips) {
		end = len(f.trips)
	}
	if offset >= len(f.trips) {
		return nil, nil, nil
	}

	page := f.trips[offset:end]
	var next map[string]types.AttributeValue
	if end < len(f.trips) {
		next = map[string]types.AttributeValue{
			"offset": &types.AttributeValueMemberN{Value: itoa(end)},
		}
	}
	return page, next, nil
}

func parseIntOrZero(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func codTrip(orderID, completedAt string, amount float64) *models.Trip {
	return &models.Trip{
		OrderID:     orderID,
		Status:      models.TripStatusCompleted,
		CompletedAt: completedAt,
		Payment:     &models.Payment{CollectCash: true, AmountZMW: amount, Method: "COD"},
	}
}

func prepaidTrip(orderID, completedAt string, amount float64) *models.Trip {
	return &models.Trip{
		OrderID:     orderID,
		Status:      models.TripStatusCompleted,
		CompletedAt: completedAt,
		Payment:     &models.Payment{CollectCash: false, AmountZMW: amount, Method: "AIRTEL_MONEY"},
	}
}

func TestFetchCashCollections_FiltersNonCOD(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{
			codTrip("ORD3", "2026-01-15T10:00:00Z", 100),
			prepaidTrip("ORD2", "2026-01-15T09:00:00Z", 50),
			codTrip("ORD1", "2026-01-15T08:00:00Z", 75),
		},
	}

	result, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.OrderCount != 2 {
		t.Fatalf("order_count = %d, want 2 (non-COD trip must be excluded)", result.OrderCount)
	}
	if result.TotalZMW != 175 {
		t.Fatalf("total_zmw = %v, want 175 (100 + 75, prepaid excluded)", result.TotalZMW)
	}
	for _, item := range result.Items {
		if item.OrderID == "ORD2" {
			t.Fatalf("non-COD order ORD2 leaked into items")
		}
	}
}

func TestFetchCashCollections_NewestFirstOrderPreserved(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{
			codTrip("ORD-NEWEST", "2026-01-15T12:00:00Z", 10),
			codTrip("ORD-MID", "2026-01-15T10:00:00Z", 20),
			codTrip("ORD-OLDEST", "2026-01-15T08:00:00Z", 30),
		},
	}

	result, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(result.Items))
	}
	want := []string{"ORD-NEWEST", "ORD-MID", "ORD-OLDEST"}
	for i, id := range want {
		if result.Items[i].OrderID != id {
			t.Errorf("items[%d].OrderID = %q, want %q (newest-first order not preserved)", i, result.Items[i].OrderID, id)
		}
	}
}

func TestFetchCashCollections_TotalsSpanAllPages(t *testing.T) {
	var trips []*models.Trip
	for i := 0; i < 250; i++ {
		trips = append(trips, codTrip("ORD"+itoa(i), "2026-01-15T10:00:00Z", 10))
	}
	lister := &fakeTripLister{trips: trips}

	// Request a small page (limit 20) but expect totals computed over the
	// FULL window (250 trips), not just the returned page.
	result, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", 0, 20)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.OrderCount != 250 {
		t.Errorf("order_count = %d, want 250 (must recompute over full window, not just first page)", result.OrderCount)
	}
	if result.TotalZMW != 2500 {
		t.Errorf("total_zmw = %v, want 2500", result.TotalZMW)
	}
	if len(result.Items) != 20 {
		t.Errorf("items returned = %d, want 20 (respecting caller's limit)", len(result.Items))
	}
	if result.NextCursor == "" {
		t.Errorf("expected a next_cursor since 250 > 20")
	}
	// The internal DynamoDB query page size (cashCollectionsQueryPageSize=100)
	// is smaller than 250 trips, so draining the window must have paged.
	if lister.pagesFetched < 3 {
		t.Errorf("pagesFetched = %d, want at least 3 (250 trips / 100 page size)", lister.pagesFetched)
	}
}

func TestFetchCashCollections_CursorPagination(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{
			codTrip("ORD-A", "2026-01-15T12:00:00Z", 10),
			codTrip("ORD-B", "2026-01-15T11:00:00Z", 10),
			codTrip("ORD-C", "2026-01-15T10:00:00Z", 10),
		},
	}

	page1, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", 0, 2)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(page1.Items) != 2 || page1.Items[0].OrderID != "ORD-A" || page1.Items[1].OrderID != "ORD-B" {
		t.Fatalf("page1 items = %+v, want [ORD-A, ORD-B]", page1.Items)
	}
	if page1.NextCursor == "" {
		t.Fatalf("expected next_cursor on page1")
	}
	if page1.OrderCount != 3 || page1.TotalZMW != 30 {
		t.Fatalf("page1 totals = (%d, %v), want (3, 30) — must reflect full window even on page 1", page1.OrderCount, page1.TotalZMW)
	}

	offset, err := decodeCashCollectionsCursor(page1.NextCursor)
	if err != nil {
		t.Fatalf("failed to decode next_cursor: %v", err)
	}
	page2, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", offset, 2)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(page2.Items) != 1 || page2.Items[0].OrderID != "ORD-C" {
		t.Fatalf("page2 items = %+v, want [ORD-C]", page2.Items)
	}
	if page2.NextCursor != "" {
		t.Errorf("page2 next_cursor = %q, want empty (no more pages)", page2.NextCursor)
	}
	if page2.OrderCount != 3 || page2.TotalZMW != 30 {
		t.Fatalf("page2 totals = (%d, %v), want (3, 30)", page2.OrderCount, page2.TotalZMW)
	}
}

func TestFetchCashCollections_EmptyWindow(t *testing.T) {
	lister := &fakeTripLister{trips: nil}

	result, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.OrderCount != 0 || result.TotalZMW != 0 {
		t.Errorf("empty window result = %+v, want zero totals", result)
	}
	if result.Items == nil {
		t.Errorf("Items should be an empty slice, not nil, so JSON encodes as [] not null")
	}
	if result.NextCursor != "" {
		t.Errorf("next_cursor = %q, want empty", result.NextCursor)
	}
}

func TestFetchCashCollections_ExcludesPaymentNil(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{
			{OrderID: "ORD-NO-PAYMENT", CompletedAt: "2026-01-15T10:00:00Z", Payment: nil},
			codTrip("ORD-COD", "2026-01-15T09:00:00Z", 40),
		},
	}

	result, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.OrderCount != 1 || result.TotalZMW != 40 {
		t.Fatalf("result = %+v, want order_count=1 total_zmw=40 (nil payment excluded)", result)
	}
}

func TestFetchCashCollections_ListerError(t *testing.T) {
	lister := &fakeTripLister{err: errors.New("dynamo unavailable")}

	_, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err == nil {
		t.Fatal("expected error to propagate from lister")
	}
}

// --- item shape sanity ------------------------------------------------

func TestFetchCashCollections_ItemFieldsExposeOrderNumberAndOrderID(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{codTrip("ORD98765", "2026-01-15T10:00:00Z", 42.5)},
	}

	result, err := fetchCashCollections(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	item := result.Items[0]
	if item.OrderNumber != "ORD98765" || item.OrderID != "ORD98765" {
		t.Errorf("item order fields = (%q, %q), want both ORD98765", item.OrderNumber, item.OrderID)
	}
	if item.DeliveredAt != "2026-01-15T10:00:00Z" {
		t.Errorf("item.DeliveredAt = %q, want completed_at value", item.DeliveredAt)
	}
	if item.AmountZMW != 42.5 {
		t.Errorf("item.AmountZMW = %v, want 42.5", item.AmountZMW)
	}
}
