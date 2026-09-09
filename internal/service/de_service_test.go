package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

type stubTodayEarningsLedgerRepo struct {
	entries           []*models.EarningsLedger
	gotDEID           string
	gotAfterTimestamp string
}

func (s *stubTodayEarningsLedgerRepo) SumPositiveCashByDEAfter(_ context.Context, deID, afterTimestamp string) (float64, error) {
	s.gotDEID = deID
	s.gotAfterTimestamp = afterTimestamp

	var total float64
	for _, entry := range s.entries {
		if models.IsPositiveCashEarning(entry) {
			total += entry.AmountZMW
		}
	}
	return total, nil
}

func TestGetTodayEarnings_SumsPositiveCashOnly(t *testing.T) {
	ledgerRepo := &stubTodayEarningsLedgerRepo{
		entries: []*models.EarningsLedger{
			{Type: models.EarningTypeTrip, AmountZMW: 100},
			{Type: models.EarningTypeB1DailyBonus, AmountZMW: 30},
			{Type: models.EarningTypeReferralBonus, AmountZMW: 25},
			{Type: models.EarningTypeMealieBag, AmountZMW: 0},
			{Type: models.EarningTypeDisbursement, AmountZMW: -50},
		},
	}
	svc := &DEService{earningsLedgerRepo: ledgerRepo}

	got, err := svc.GetTodayEarnings(context.Background(), "de-1")
	if err != nil {
		t.Fatalf("GetTodayEarnings returned error: %v", err)
	}
	if got != 155 {
		t.Fatalf("today earnings = %v, want 155", got)
	}
	if ledgerRepo.gotDEID != "de-1" {
		t.Fatalf("deID = %q, want de-1", ledgerRepo.gotDEID)
	}
	if ledgerRepo.gotAfterTimestamp != timezone.StartOfDayString() {
		t.Fatalf("after timestamp = %q, want %q", ledgerRepo.gotAfterTimestamp, timezone.StartOfDayString())
	}
}

type archiveDERepo struct {
	de          *models.DeliveryExecutive
	getErr      error
	updateErr   error
	statusCalls []string
	archiveOps  []bool
	setErr      error
}

func (s *archiveDERepo) Create(context.Context, *models.DeliveryExecutive) error {
	return nil
}
func (s *archiveDERepo) GetByPhone(context.Context, string) (*models.DeliveryExecutive, error) {
	return s.de, s.getErr
}
func (s *archiveDERepo) UpdateAssignedStore(context.Context, string, string) error {
	return nil
}
func (s *archiveDERepo) ListByAssignedStore(context.Context, string, string, string, int32) ([]*models.DeliveryExecutive, string, error) {
	return nil, "", nil
}
func (s *archiveDERepo) MarkEligibleFromScan(context.Context, string, string, float64, float64, string) error {
	return nil
}
func (s *archiveDERepo) UpdateStatus(_ context.Context, phone string, status models.DEStatus, _, _ string) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.statusCalls = append(s.statusCalls, phone+":"+string(status))
	if s.de != nil {
		s.de.Status = status
	}
	return nil
}
func (s *archiveDERepo) SetArchived(_ context.Context, _ string, archived bool) (*models.DeliveryExecutive, error) {
	s.archiveOps = append(s.archiveOps, archived)
	if s.setErr != nil {
		return nil, s.setErr
	}
	if s.de != nil {
		s.de.Archived = archived
		if archived {
			s.de.ArchivedAt = "2026-09-09T12:00:00Z"
		} else {
			s.de.ArchivedAt = ""
		}
	}
	return s.de, nil
}

type recordingStatusEvents struct {
	events []*models.DEStatusEvent
}

func (r *recordingStatusEvents) Append(_ context.Context, event *models.DEStatusEvent) error {
	clone := *event
	r.events = append(r.events, &clone)
	return nil
}

func testArchiveLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func TestArchiveDriver_Busy(t *testing.T) {
	repo := &archiveDERepo{de: &models.DeliveryExecutive{
		PhoneNumber: "+260971000001",
		Status:      models.DEStatusBusy,
	}}
	svc := &DEService{deRepo: repo, logger: testArchiveLogger()}

	_, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if !errors.Is(err, ErrDEBusyArchive) {
		t.Fatalf("err = %v, want ErrDEBusyArchive", err)
	}
	if len(repo.archiveOps) != 0 || len(repo.statusCalls) != 0 {
		t.Fatalf("busy archive must not write status=%v archive=%v", repo.statusCalls, repo.archiveOps)
	}
}

func TestArchiveDriver_Eligible(t *testing.T) {
	repo := &archiveDERepo{de: &models.DeliveryExecutive{
		PhoneNumber:    "+260971000001",
		Status:         models.DEStatusEligible,
		CurrentStoreID: "221",
	}}
	events := &recordingStatusEvents{}
	svc := &DEService{deRepo: repo, statusEventRepo: events, logger: testArchiveLogger()}

	got, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if err != nil {
		t.Fatalf("ArchiveDriver: %v", err)
	}
	if got == nil || !got.Archived {
		t.Fatalf("got %#v", got)
	}
	if got.Status != models.DEStatusOffline {
		t.Fatalf("status = %q, want offline", got.Status)
	}
	if len(repo.statusCalls) != 1 || repo.statusCalls[0] != "+260971000001:offline" {
		t.Fatalf("statusCalls = %v", repo.statusCalls)
	}
	if len(repo.archiveOps) != 1 || !repo.archiveOps[0] {
		t.Fatalf("archiveOps = %v", repo.archiveOps)
	}
	if len(events.events) != 1 {
		t.Fatalf("events = %d, want 1", len(events.events))
	}
	ev := events.events[0]
	if ev.FromState != models.DEStatusEligible || ev.ToState != models.DEStatusOffline || ev.Reason != models.ReasonEndedDuty {
		t.Fatalf("event = %#v", ev)
	}
	if ev.StoreID != "221" {
		t.Fatalf("event store = %q", ev.StoreID)
	}
}

func TestArchiveDriver_Offline(t *testing.T) {
	repo := &archiveDERepo{de: &models.DeliveryExecutive{
		PhoneNumber: "+260971000001",
		Status:      models.DEStatusOffline,
	}}
	events := &recordingStatusEvents{}
	svc := &DEService{deRepo: repo, statusEventRepo: events, logger: testArchiveLogger()}

	got, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if err != nil {
		t.Fatalf("ArchiveDriver: %v", err)
	}
	if got == nil || !got.Archived {
		t.Fatalf("got %#v", got)
	}
	if len(repo.statusCalls) != 0 {
		t.Fatalf("offline archive must not UpdateStatus, got %v", repo.statusCalls)
	}
	if len(events.events) != 0 {
		t.Fatalf("offline archive must not append status event")
	}
	if len(repo.archiveOps) != 1 || !repo.archiveOps[0] {
		t.Fatalf("archiveOps = %v", repo.archiveOps)
	}
}

func TestArchiveDriver_AlreadyArchived(t *testing.T) {
	repo := &archiveDERepo{de: &models.DeliveryExecutive{
		PhoneNumber: "+260971000001",
		Status:      models.DEStatusOffline,
		Archived:    true,
		ArchivedAt:  "2026-01-01T00:00:00Z",
	}}
	svc := &DEService{deRepo: repo, logger: testArchiveLogger()}

	got, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if err != nil {
		t.Fatalf("ArchiveDriver: %v", err)
	}
	if got == nil || !got.Archived || got.ArchivedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("got %#v", got)
	}
	if len(repo.archiveOps) != 0 || len(repo.statusCalls) != 0 {
		t.Fatalf("idempotent archive must not write, status=%v archive=%v", repo.statusCalls, repo.archiveOps)
	}
}

func TestRestoreDriver(t *testing.T) {
	repo := &archiveDERepo{de: &models.DeliveryExecutive{
		PhoneNumber: "+260971000001",
		Status:      models.DEStatusOffline,
		Archived:    true,
		ArchivedAt:  "2026-01-01T00:00:00Z",
	}}
	svc := &DEService{deRepo: repo, logger: testArchiveLogger()}

	got, err := svc.RestoreDriver(context.Background(), "+260971000001")
	if err != nil {
		t.Fatalf("RestoreDriver: %v", err)
	}
	if got == nil || got.Archived || got.ArchivedAt != "" {
		t.Fatalf("got %#v", got)
	}
	if got.Status != models.DEStatusOffline {
		t.Fatalf("status = %q, want offline", got.Status)
	}
	if len(repo.archiveOps) != 1 || repo.archiveOps[0] {
		t.Fatalf("archiveOps = %v, want [false]", repo.archiveOps)
	}
	if len(repo.statusCalls) != 0 {
		t.Fatalf("restore must not change status, got %v", repo.statusCalls)
	}
}

func TestArchiveDriver_Free(t *testing.T) {
	repo := &archiveDERepo{de: &models.DeliveryExecutive{
		PhoneNumber:    "+260971000001",
		Status:         models.DEStatusFree,
		CurrentStoreID: "221",
	}}
	events := &recordingStatusEvents{}
	svc := &DEService{deRepo: repo, statusEventRepo: events, logger: testArchiveLogger()}

	got, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if err != nil {
		t.Fatalf("ArchiveDriver: %v", err)
	}
	if got == nil || !got.Archived || got.Status != models.DEStatusOffline {
		t.Fatalf("got %#v", got)
	}
	if len(events.events) != 1 || events.events[0].FromState != models.DEStatusFree {
		t.Fatalf("events = %#v", events.events)
	}
}

func TestArchiveDriver_NotFound(t *testing.T) {
	svc := &DEService{deRepo: &archiveDERepo{}, logger: testArchiveLogger()}
	_, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if !errors.Is(err, repository.ErrDENotFound) {
		t.Fatalf("err = %v, want ErrDENotFound", err)
	}
}

func TestArchiveDriver_GetError(t *testing.T) {
	want := errors.New("boom")
	svc := &DEService{deRepo: &archiveDERepo{getErr: want}, logger: testArchiveLogger()}
	_, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestArchiveDriver_UpdateStatusError(t *testing.T) {
	repo := &archiveDERepo{
		de: &models.DeliveryExecutive{PhoneNumber: "+260971000001", Status: models.DEStatusEligible},
	}
	repo.updateErr = errors.New("status write failed")
	svc := &DEService{deRepo: repo, logger: testArchiveLogger()}
	_, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if !errors.Is(err, repo.updateErr) {
		t.Fatalf("err = %v", err)
	}
	if len(repo.archiveOps) != 0 {
		t.Fatal("must not archive after status update failure")
	}
}

func TestArchiveDriver_SetArchivedError(t *testing.T) {
	want := errors.New("archive write failed")
	repo := &archiveDERepo{
		de:     &models.DeliveryExecutive{PhoneNumber: "+260971000001", Status: models.DEStatusOffline},
		setErr: want,
	}
	svc := &DEService{deRepo: repo, logger: testArchiveLogger()}
	_, err := svc.ArchiveDriver(context.Background(), "+260971000001")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreDriver_NotFound(t *testing.T) {
	svc := &DEService{deRepo: &archiveDERepo{}, logger: testArchiveLogger()}
	_, err := svc.RestoreDriver(context.Background(), "+260971000001")
	if !errors.Is(err, repository.ErrDENotFound) {
		t.Fatalf("err = %v, want ErrDENotFound", err)
	}
}

func TestRestoreDriver_GetError(t *testing.T) {
	want := errors.New("boom")
	svc := &DEService{deRepo: &archiveDERepo{getErr: want}, logger: testArchiveLogger()}
	_, err := svc.RestoreDriver(context.Background(), "+260971000001")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreDriver_SetArchivedError(t *testing.T) {
	want := errors.New("restore write failed")
	repo := &archiveDERepo{
		de: &models.DeliveryExecutive{
			PhoneNumber: "+260971000001",
			Status:      models.DEStatusOffline,
			Archived:    true,
		},
		setErr: want,
	}
	svc := &DEService{deRepo: repo, logger: testArchiveLogger()}
	_, err := svc.RestoreDriver(context.Background(), "+260971000001")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreDriver_RestoreIdempotent(t *testing.T) {
	repo := &archiveDERepo{de: &models.DeliveryExecutive{
		PhoneNumber: "+260971000001",
		Status:      models.DEStatusOffline,
		Archived:    false,
	}}
	svc := &DEService{deRepo: repo, logger: testArchiveLogger()}

	got, err := svc.RestoreDriver(context.Background(), "+260971000001")
	if err != nil {
		t.Fatalf("RestoreDriver: %v", err)
	}
	if got == nil || got.Archived {
		t.Fatalf("got %#v", got)
	}
	if len(repo.archiveOps) != 0 {
		t.Fatalf("idempotent restore must not write, got %v", repo.archiveOps)
	}
}
