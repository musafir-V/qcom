package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/models"
)

var (
	ErrInvalidDisputeStatus     = errors.New("invalid dispute status")
	ErrInvalidDisputeTransition = errors.New("dispute cannot move to that status")
	ErrResolutionNoteRequired   = errors.New("resolution note is required")
)

type adminDisputeStore interface {
	GetByID(ctx context.Context, id string) (*models.Dispute, error)
	ListByStatus(ctx context.Context, status models.DisputeStatus, cursor string, limit int32) ([]models.Dispute, string, error)
	CountByStatus(ctx context.Context, status models.DisputeStatus) (int, error)
	UpdateStatus(ctx context.Context, id string, newStatus models.DisputeStatus, note, actor, now string) (*models.Dispute, error)
}

type dispositionLookup interface {
	GetByCode(ctx context.Context, code string) (*models.DisputeDisposition, error)
}

// DisputeSummary holds per-status counts used by the dashboard badge + tabs.
type DisputeSummary struct {
	Open        int `json:"open"`
	UnderReview int `json:"under_review"`
	Resolved    int `json:"resolved"`
	Rejected    int `json:"rejected"`
}

type AdminDisputeService struct {
	disputes     adminDisputeStore
	dispositions dispositionLookup
}

func NewAdminDisputeService(disputes adminDisputeStore, dispositions dispositionLookup) *AdminDisputeService {
	return &AdminDisputeService{disputes: disputes, dispositions: dispositions}
}

func validDisputeStatus(s models.DisputeStatus) bool {
	switch s {
	case models.DisputeStatusOpen, models.DisputeStatusUnderReview,
		models.DisputeStatusResolved, models.DisputeStatusRejected:
		return true
	}
	return false
}

func (s *AdminDisputeService) ListByStatus(ctx context.Context, status models.DisputeStatus, cursor string, limit int32) ([]models.Dispute, string, error) {
	if !validDisputeStatus(status) {
		return nil, "", ErrInvalidDisputeStatus
	}
	return s.disputes.ListByStatus(ctx, status, cursor, limit)
}

func (s *AdminDisputeService) Summary(ctx context.Context) (DisputeSummary, error) {
	var sum DisputeSummary
	targets := []struct {
		st models.DisputeStatus
		p  *int
	}{
		{models.DisputeStatusOpen, &sum.Open},
		{models.DisputeStatusUnderReview, &sum.UnderReview},
		{models.DisputeStatusResolved, &sum.Resolved},
		{models.DisputeStatusRejected, &sum.Rejected},
	}
	for _, t := range targets {
		n, err := s.disputes.CountByStatus(ctx, t.st)
		if err != nil {
			return DisputeSummary{}, err
		}
		*t.p = n
	}
	return sum, nil
}

func (s *AdminDisputeService) UpdateStatus(ctx context.Context, id string, newStatus models.DisputeStatus, note, actor string) (*models.Dispute, error) {
	if !validDisputeStatus(newStatus) {
		return nil, ErrInvalidDisputeStatus
	}
	d, err := s.disputes.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, ErrDisputeNotFound
	}
	if !models.CanTransitionDispute(d.Status, newStatus) {
		return nil, ErrInvalidDisputeTransition
	}
	note = strings.TrimSpace(note)
	if (newStatus == models.DisputeStatusResolved || newStatus == models.DisputeStatusRejected) && note == "" {
		return nil, ErrResolutionNoteRequired
	}
	now := time.Now().UTC().Format(time.RFC3339)
	updated, err := s.disputes.UpdateStatus(ctx, id, newStatus, note, actor, now)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrDisputeNotFound
	}
	return updated, nil
}

// TitlesFor resolves disposition codes to human titles, deduping and skipping unknowns.
func (s *AdminDisputeService) TitlesFor(ctx context.Context, codes []string) map[string]string {
	out := make(map[string]string)
	for _, code := range codes {
		if code == "" || out[code] != "" {
			continue
		}
		disp, err := s.dispositions.GetByCode(ctx, code)
		if err != nil || disp == nil {
			continue
		}
		out[code] = disp.Title
	}
	return out
}
