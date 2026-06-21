package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type stubUseCaseStore struct {
	entry *models.UploadUseCase
}

func (s *stubUseCaseStore) GetByUseCase(_ context.Context, useCase string) (*models.UploadUseCase, error) {
	if s.entry != nil && s.entry.UseCase == useCase {
		return s.entry, nil
	}
	return nil, nil
}

func newTestUploadService(entry *models.UploadUseCase) *UploadService {
	return &UploadService{
		registry:      &stubUseCaseStore{entry: entry},
		presignExpiry: 5 * time.Minute,
		logger:        logrus.New(),
	}
}

func disputePhotoEntry() *models.UploadUseCase {
	return &models.UploadUseCase{
		UseCase:            "dispute_photo",
		Bucket:             "printdrop-documents",
		KeyPrefix:          "disputes",
		AllowedMimeTypes:   []string{"image/jpeg", "image/png"},
		MaxFileSize:        10 * 1024 * 1024,
		AllowedEntityTypes: []string{"customer"},
	}
}

func TestGeneratePresignedURL_UnknownUseCase(t *testing.T) {
	svc := newTestUploadService(nil)
	_, err := svc.GeneratePresignedURL(context.Background(), "nope", "customer", "u1", "a.jpg", "image/jpeg", 100)
	if !errors.Is(err, ErrUnknownUseCase) {
		t.Fatalf("want ErrUnknownUseCase, got %v", err)
	}
}

func TestGeneratePresignedURL_EntityTypeDenied(t *testing.T) {
	svc := newTestUploadService(disputePhotoEntry())
	_, err := svc.GeneratePresignedURL(context.Background(), "dispute_photo", "de", "u1", "a.jpg", "image/jpeg", 100)
	if !errors.Is(err, ErrEntityTypeNotAllowed) {
		t.Fatalf("want ErrEntityTypeNotAllowed, got %v", err)
	}
}

func TestGeneratePresignedURL_MimeDenied(t *testing.T) {
	svc := newTestUploadService(disputePhotoEntry())
	_, err := svc.GeneratePresignedURL(context.Background(), "dispute_photo", "customer", "u1", "a.pdf", "application/pdf", 100)
	if !errors.Is(err, ErrMimeNotAllowed) {
		t.Fatalf("want ErrMimeNotAllowed, got %v", err)
	}
}

func TestGeneratePresignedURL_TooLarge(t *testing.T) {
	svc := newTestUploadService(disputePhotoEntry())
	_, err := svc.GeneratePresignedURL(context.Background(), "dispute_photo", "customer", "u1", "a.jpg", "image/jpeg", 11*1024*1024)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("want ErrFileTooLarge, got %v", err)
	}
}
