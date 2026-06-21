package service

import (
	"context"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// DisputeNotifier is the seam for reacting to a newly created dispute.
// v1 only logs; a later admin-notification implementation replaces it.
type DisputeNotifier interface {
	DisputeCreated(ctx context.Context, d *models.Dispute)
}

type LoggingDisputeNotifier struct {
	logger *logrus.Logger
}

func NewLoggingDisputeNotifier(logger *logrus.Logger) *LoggingDisputeNotifier {
	return &LoggingDisputeNotifier{logger: logger}
}

func (n *LoggingDisputeNotifier) DisputeCreated(ctx context.Context, d *models.Dispute) {
	n.logger.WithFields(logrus.Fields{
		"event":            "dispute_created",
		"dispute_id":       d.DisputeID,
		"order_id":         d.OrderID,
		"customer_id":      d.CustomerID,
		"disposition_code": d.DispositionCode,
	}).Info("dispute created")
}
