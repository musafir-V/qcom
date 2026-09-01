package service

import (
	"context"
	"errors"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestCompleteByOrder_NoTrip_NoOp(t *testing.T) {
	repo := &stubTripRepo{trip: nil}
	svc := newTestTripService(repo, &stubNotifier{})

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-NONE",
		Status:  "OUT_FOR_DELIVERY",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Updated || res.Reason != "no_active_trip" {
		t.Fatalf("expected no_active_trip no-op, got %+v", res)
	}
}

func TestCompleteByOrder_InvalidStatus(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T1",
		OrderID: "ORD-1",
		Status:  models.TripStatusAccepted,
	}}
	svc := newTestTripService(repo, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-1",
		Status:  "PACKING",
	})
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("expected ErrInvalidStatus, got %v", err)
	}
}
