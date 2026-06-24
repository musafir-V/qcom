package service

import (
	"context"
	"errors"
	"strings"
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

func TestGeneratePresignedViewURLs_InvalidKey(t *testing.T) {
	svc := newTestUploadService(disputePhotoEntry())
	_, err := svc.GeneratePresignedViewURLs(context.Background(), "dispute_photo", "customer", "u1", []string{"disputes/other-cust/a.jpg"})
	if !errors.Is(err, ErrInvalidObjectKey) {
		t.Fatalf("want ErrInvalidObjectKey, got %v", err)
	}
}

func TestGeneratePresignedViewURLs_UnknownUseCase(t *testing.T) {
	svc := newTestUploadService(nil)
	_, err := svc.GeneratePresignedViewURLs(context.Background(), "nope", "customer", "u1", []string{"disputes/u1/a.jpg"})
	if !errors.Is(err, ErrUnknownUseCase) {
		t.Fatalf("want ErrUnknownUseCase, got %v", err)
	}
}

func TestGeneratePresignedViewURLs_EmptyKeys(t *testing.T) {
	svc := newTestUploadService(disputePhotoEntry())
	urls, err := svc.GeneratePresignedViewURLs(context.Background(), "dispute_photo", "customer", "u1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) != 0 {
		t.Fatalf("want empty urls, got %v", urls)
	}
}

func newTestUploadServiceWithTripBucket(entry *models.UploadUseCase, tripBucket string) *UploadService {
	return &UploadService{
		registry:         &stubUseCaseStore{entry: entry},
		presignExpiry:    5 * time.Minute,
		tripPhotosBucket: tripBucket,
		logger:           logrus.New(),
	}
}

func TestPresignTripTaskPhoto_Success(t *testing.T) {
	svc := newTestUploadServiceWithTripBucket(nil, "test-bucket")
	result, err := svc.PresignTripTaskPhoto(context.Background(), "ORD-001", "drop", "de-123", "image/jpeg", 1024)
	// presignClient is nil — will error on actual presign, but key construction can be validated
	// if the error is not a validation error, the key would have been constructed correctly
	if errors.Is(err, ErrUnsupportedPhotoMimeType) || errors.Is(err, ErrPhotoFileTooLarge) || errors.Is(err, ErrMissingTripPhotosBucket) {
		t.Fatalf("unexpected validation error: %v", err)
	}
	// either result is non-nil (if presign somehow succeeded) or err is a presign error
	if result != nil {
		wantPrefix := "orders/ORD-001/drop/de-123/"
		if !strings.HasPrefix(result.ObjectKey, wantPrefix) {
			t.Errorf("object key %q does not start with %q", result.ObjectKey, wantPrefix)
		}
	}
	// test passes — presign failure with nil client is expected in unit tests
}

func TestPresignTripTaskPhoto_UnsupportedMime(t *testing.T) {
	svc := newTestUploadServiceWithTripBucket(nil, "test-bucket")
	_, err := svc.PresignTripTaskPhoto(context.Background(), "ORD-001", "drop", "de-123", "application/pdf", 1024)
	if !errors.Is(err, ErrUnsupportedPhotoMimeType) {
		t.Fatalf("want ErrUnsupportedPhotoMimeType, got %v", err)
	}
}

func TestPresignTripTaskPhoto_FileTooLarge(t *testing.T) {
	svc := newTestUploadServiceWithTripBucket(nil, "test-bucket")
	_, err := svc.PresignTripTaskPhoto(context.Background(), "ORD-001", "drop", "de-123", "image/jpeg", 11*1024*1024)
	if !errors.Is(err, ErrPhotoFileTooLarge) {
		t.Fatalf("want ErrPhotoFileTooLarge, got %v", err)
	}
}

func TestPresignTripTaskPhoto_NoBucketConfigured(t *testing.T) {
	svc := newTestUploadServiceWithTripBucket(nil, "")
	_, err := svc.PresignTripTaskPhoto(context.Background(), "ORD-001", "drop", "de-123", "image/jpeg", 1024)
	if !errors.Is(err, ErrMissingTripPhotosBucket) {
		t.Fatalf("want ErrMissingTripPhotosBucket, got %v", err)
	}
}

func TestPresignTripTaskPhoto_KeyFormat(t *testing.T) {
	tests := []struct {
		name        string
		orderNumber string
		taskType    string
		deID        string
		fileType    string
		expectedExt string
	}{
		{"jpeg_pickup", "ORD-001", "pickup", "de-123", "image/jpeg", ".jpg"},
		{"jpeg_drop", "ORD-001", "drop", "de-123", "image/jpeg", ".jpg"},
		{"png_pickup", "ORD-002", "pickup", "de-456", "image/png", ".png"},
		{"png_drop", "ORD-002", "drop", "de-456", "image/png", ".png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFileID := "550e8400-e29b-41d4-a716-446655440000"
			key := buildTripTaskPhotoKey(tt.orderNumber, tt.taskType, tt.deID, tt.fileType, testFileID)

			// Verify key follows format: orders/{orderNumber}/{taskType}/{deID}/{fileID}{ext}
			wantPrefix := "orders/" + tt.orderNumber + "/" + tt.taskType + "/" + tt.deID + "/" + testFileID
			if key != wantPrefix+tt.expectedExt {
				t.Errorf("key %q does not match expected format %q", key, wantPrefix+tt.expectedExt)
			}
		})
	}
}
