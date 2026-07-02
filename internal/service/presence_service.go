package service

import (
	"context"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

// presenceEventLister reads a DE's status-event log for a Zambia calendar day.
type presenceEventLister interface {
	ListEventsForDay(ctx context.Context, phone, zambiaDate string) ([]*models.DEStatusEvent, error)
}

// PresenceService reconstructs a rider's online timeline for a day from the
// status-event log. Online = a contiguous run in {eligible, busy, free}; a
// segment ends at the next offline event (missed_scan / ended_duty).
type PresenceService struct {
	statusEventRepo presenceEventLister
	logger          *logrus.Logger
}

func NewPresenceService(statusEventRepo presenceEventLister, logger *logrus.Logger) *PresenceService {
	return &PresenceService{statusEventRepo: statusEventRepo, logger: logger}
}

// PresenceSegment is one online interval, with times as "HH:MM" in Zambia local.
type PresenceSegment struct {
	Start     string `json:"start"`
	End       string `json:"end"`
	EndReason string `json:"end_reason"`
	StoreID   string `json:"store_id,omitempty"`
}

// PresenceReport is the per-day presence summary returned by the admin API.
type PresenceReport struct {
	Date               string            `json:"date"`
	TotalOnlineMinutes int               `json:"total_online_minutes"`
	Segments           []PresenceSegment `json:"segments"`
}

// reasonOngoing marks a segment that is still open at the end of the queried
// window (no offline event yet).
const reasonOngoing = "ongoing"

// GetDayPresence computes the online segments and total online minutes for a DE
// on the given Zambia date ("2006-01-02"). Pass "" to default to today (Zambia).
func (s *PresenceService) GetDayPresence(ctx context.Context, phone, date string) (*PresenceReport, error) {
	op := logging.Start(ctx, s.logger, "PresenceService.GetDayPresence", logrus.Fields{
		"phone": phone, "date": date,
	})
	defer op.End()

	if date == "" {
		date = timezone.DateString()
	}

	loc := timezone.ZambiaLocation()
	dayStart, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, op.Outcome("bad_date", err)
	}
	dayEnd := dayStart.AddDate(0, 0, 1)

	events, err := s.statusEventRepo.ListEventsForDay(ctx, phone, date)
	if err != nil {
		return nil, op.Fail(err)
	}

	report := &PresenceReport{Date: date, Segments: []PresenceSegment{}}

	var (
		open     bool
		segStart time.Time
		segStore string
		total    time.Duration
	)
	closeSegment := func(end time.Time, reason string, store string) {
		if end.Before(segStart) {
			end = segStart
		}
		report.Segments = append(report.Segments, PresenceSegment{
			Start:     segStart.In(loc).Format("15:04"),
			End:       end.In(loc).Format("15:04"),
			EndReason: reason,
			StoreID:   store,
		})
		total += end.Sub(segStart)
		open = false
	}

	for _, e := range events {
		t, perr := time.Parse(time.RFC3339, e.TS)
		if perr != nil {
			s.logger.WithError(perr).WithField("phone", phone).Warn("presence: skipping event with bad ts")
			continue
		}

		if e.ToState.IsOnline() {
			if !open {
				// If we were already online before this event (from_state online),
				// the run began before the day boundary; credit from day start.
				if e.FromState.IsOnline() {
					segStart = dayStart
				} else {
					segStart = t
				}
				segStore = e.StoreID
				open = true
			}
			continue
		}

		// Transition to offline.
		if open {
			closeSegment(t, string(e.Reason), segStore)
		} else if e.FromState.IsOnline() {
			// Online since before midnight, went offline this day with no prior
			// opening event in-window; credit from day start.
			segStart = dayStart
			segStore = e.StoreID
			open = true
			closeSegment(t, string(e.Reason), segStore)
		}
	}

	if open {
		// Still online at the end of the window. Cap at now (if the day is today)
		// or the day boundary otherwise.
		end := dayEnd
		if now := timezone.Now(); now.Before(dayEnd) {
			end = now
		}
		closeSegment(end, reasonOngoing, segStore)
	}

	report.TotalOnlineMinutes = int(total.Minutes())
	op.With("segments", len(report.Segments)).With("total_online_minutes", report.TotalOnlineMinutes)
	return report, nil
}
