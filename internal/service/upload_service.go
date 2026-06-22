package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

var (
	ErrUnknownUseCase       = errors.New("unknown upload use case")
	ErrEntityTypeNotAllowed = errors.New("entity type not allowed for this use case")
	ErrMimeNotAllowed       = errors.New("file_type not allowed for this use case")
	ErrFileTooLarge         = errors.New("file_size exceeds maximum for this use case")
	ErrInvalidFileRequest   = errors.New("invalid file request")
	ErrInvalidObjectKey     = errors.New("object key is not owned by this entity")
)

// mimeExtensions maps an allowed MIME type to its canonical file extension.
var mimeExtensions = map[string]string{
	"application/pdf": ".pdf",
	"application/msword": ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/heic": ".heic",
}

// UploadUseCaseStore loads upload use-case registry entries.
type UploadUseCaseStore interface {
	GetByUseCase(ctx context.Context, useCase string) (*models.UploadUseCase, error)
}

type UploadService struct {
	presignClient *s3.PresignClient
	registry      UploadUseCaseStore
	presignExpiry time.Duration
	logger        *logrus.Logger
}

type PresignedUploadResult struct {
	FileID           string
	UploadURL        string
	ObjectKey        string
	ExpiresInSeconds int
	MaxFileSize      int64
}

type PresignedViewResult struct {
	ViewURL          string
	ExpiresInSeconds int
}

func NewUploadService(presignClient *s3.PresignClient, registry UploadUseCaseStore, presignExpiry time.Duration, logger *logrus.Logger) *UploadService {
	return &UploadService{
		presignClient: presignClient,
		registry:      registry,
		presignExpiry: presignExpiry,
		logger:        logger,
	}
}

// GeneratePresignedURL validates the request against the use case's registry entry
// and returns a presigned PUT URL into the configured bucket/prefix.
func (s *UploadService) GeneratePresignedURL(ctx context.Context, useCase, entityType, entityID, fileName, fileType string, fileSize int64) (*PresignedUploadResult, error) {
	op := logging.Start(ctx, s.logger, "GeneratePresignedURL", logrus.Fields{
		"use_case": useCase, "entity_id": entityID, "file_type": fileType, "file_size": fileSize,
	})
	defer op.End()

	if strings.TrimSpace(fileName) == "" || strings.TrimSpace(fileType) == "" || fileSize <= 0 {
		return nil, op.Fail(fmt.Errorf("%w: file_name, file_type and positive file_size are required", ErrInvalidFileRequest))
	}

	entry, err := s.registry.GetByUseCase(ctx, useCase)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to load use case: %w", err))
	}
	if entry == nil {
		return nil, op.Fail(fmt.Errorf("%w: %q", ErrUnknownUseCase, useCase))
	}
	if !entry.AllowsEntityType(entityType) {
		return nil, op.Fail(fmt.Errorf("%w: %q", ErrEntityTypeNotAllowed, entityType))
	}
	if !entry.AllowsMime(fileType) {
		return nil, op.Fail(fmt.Errorf("%w: %q", ErrMimeNotAllowed, fileType))
	}
	if fileSize > entry.MaxFileSize {
		return nil, op.Fail(fmt.Errorf("%w: %d > %d", ErrFileTooLarge, fileSize, entry.MaxFileSize))
	}

	ext := mimeExtensions[fileType]
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(fileName))
	}

	fileID := uuid.New().String()
	objectKey := fmt.Sprintf("%s/%s/%s%s", entry.KeyPrefix, entityID, fileID, ext)

	presignResult, err := s.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(entry.Bucket),
		Key:           aws.String(objectKey),
		ContentType:   aws.String(fileType),
		ContentLength: aws.Int64(fileSize),
	}, s3.WithPresignExpires(s.presignExpiry))
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to generate presigned URL: %w", err))
	}

	return &PresignedUploadResult{
		FileID:           fileID,
		UploadURL:        presignResult.URL,
		ObjectKey:        objectKey,
		ExpiresInSeconds: int(s.presignExpiry.Seconds()),
		MaxFileSize:      entry.MaxFileSize,
	}, nil
}

// GeneratePresignedViewURL returns a short-lived GET URL for an object the caller owns.
func (s *UploadService) GeneratePresignedViewURL(ctx context.Context, useCase, entityType, entityID, objectKey string) (*PresignedViewResult, error) {
	entry, err := s.loadUseCaseForObjectKey(ctx, useCase, entityType, entityID, objectKey)
	if err != nil {
		return nil, err
	}
	url, err := s.presignGetObject(ctx, entry.Bucket, objectKey)
	if err != nil {
		return nil, err
	}
	return &PresignedViewResult{
		ViewURL:          url,
		ExpiresInSeconds: int(s.presignExpiry.Seconds()),
	}, nil
}

// GeneratePresignedViewURLs presigns GET access for each object key in order.
func (s *UploadService) GeneratePresignedViewURLs(ctx context.Context, useCase, entityType, entityID string, objectKeys []string) ([]string, error) {
	if len(objectKeys) == 0 {
		return nil, nil
	}
	entry, err := s.registry.GetByUseCase(ctx, useCase)
	if err != nil {
		return nil, fmt.Errorf("failed to load use case: %w", err)
	}
	if entry == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownUseCase, useCase)
	}
	if !entry.AllowsEntityType(entityType) {
		return nil, fmt.Errorf("%w: %q", ErrEntityTypeNotAllowed, entityType)
	}
	wantPrefix := entry.KeyPrefix + "/" + entityID + "/"
	urls := make([]string, 0, len(objectKeys))
	for _, key := range objectKeys {
		if !strings.HasPrefix(key, wantPrefix) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidObjectKey, key)
		}
		url, err := s.presignGetObject(ctx, entry.Bucket, key)
		if err != nil {
			return nil, err
		}
		urls = append(urls, url)
	}
	return urls, nil
}

func (s *UploadService) loadUseCaseForObjectKey(ctx context.Context, useCase, entityType, entityID, objectKey string) (*models.UploadUseCase, error) {
	if strings.TrimSpace(objectKey) == "" {
		return nil, fmt.Errorf("%w: empty object key", ErrInvalidObjectKey)
	}
	entry, err := s.registry.GetByUseCase(ctx, useCase)
	if err != nil {
		return nil, fmt.Errorf("failed to load use case: %w", err)
	}
	if entry == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownUseCase, useCase)
	}
	if !entry.AllowsEntityType(entityType) {
		return nil, fmt.Errorf("%w: %q", ErrEntityTypeNotAllowed, entityType)
	}
	wantPrefix := entry.KeyPrefix + "/" + entityID + "/"
	if !strings.HasPrefix(objectKey, wantPrefix) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidObjectKey, objectKey)
	}
	return entry, nil
}

func (s *UploadService) presignGetObject(ctx context.Context, bucket, objectKey string) (string, error) {
	if s.presignClient == nil {
		return "", fmt.Errorf("presign client not configured")
	}
	presignResult, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	}, s3.WithPresignExpires(s.presignExpiry))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned view URL: %w", err)
	}
	return presignResult.URL, nil
}
