package service

import (
	"context"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

func TestLoggingDisputeNotifier_DoesNotPanic(t *testing.T) {
	n := NewLoggingDisputeNotifier(logrus.New())
	// Must satisfy the interface and be safe to call.
	var _ DisputeNotifier = n
	n.DisputeCreated(context.Background(), &models.Dispute{DisputeID: "d1", OrderID: "o1", DispositionCode: "ITEM_MISSING"})
}
