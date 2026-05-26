package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const maxFileSize int64 = 50 * 1024 * 1024 // 50 MB

var allowedMimeTypes = map[string]string{
	"application/pdf":                                                          "pdf",
	"application/msword":                                                       "doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":  "docx",
	"image/jpeg":                                                               "jpg",
	"image/png":                                                                "png",
	"image/heic":                                                               "heic",
}

var allowedExtensions = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true,
	".jpg": true, ".jpeg": true, ".png": true, ".heic": true,
}

type UploadService struct {
	presignClient  *s3.PresignClient
	bucket         string
	presignExpiry  time.Duration
	logger         *logrus.Logger
}

type PresignedUploadResult struct {
	FileID           string
	UploadURL        string
	ObjectKey        string
	ExpiresInSeconds int
	MaxFileSize      int64
}

func NewUploadService(s3Client *s3.Client, cfg *config.S3Config, logger *logrus.Logger) *UploadService {
	return &UploadService{
		presignClient: s3.NewPresignClient(s3Client),
		bucket:        cfg.Bucket,
		presignExpiry: time.Duration(cfg.PresignExpirySeconds) * time.Second,
		logger:        logger,
	}
}

func (s *UploadService) ValidateFileRequest(fileName, fileType string, fileSize int64) error {
	if strings.TrimSpace(fileName) == "" {
		return fmt.Errorf("file_name is required")
	}

	if strings.TrimSpace(fileType) == "" {
		return fmt.Errorf("file_type is required")
	}

	if fileSize <= 0 {
		return fmt.Errorf("file_size must be greater than 0")
	}

	if fileSize > maxFileSize {
		return fmt.Errorf("file_size exceeds maximum allowed size of %d bytes (50 MB)", maxFileSize)
	}

	if _, ok := allowedMimeTypes[fileType]; !ok {
		return fmt.Errorf("file_type %q is not allowed; accepted types: pdf, doc, docx, jpg, png, heic", fileType)
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		return fmt.Errorf("file_name must have an extension")
	}
	if !allowedExtensions[ext] {
		return fmt.Errorf("file extension %q is not allowed", ext)
	}

	return nil
}

func (s *UploadService) GeneratePresignedURL(ctx context.Context, userID, fileName, fileType string, fileSize int64) (*PresignedUploadResult, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":        "GeneratePresignedURL",
		"user_id":   userID,
		"file_name": fileName,
		"file_type": fileType,
		"file_size": fileSize,
	}).Info("service call start")

	fileID := uuid.New().String()
	ext := strings.ToLower(filepath.Ext(fileName))
	objectKey := fmt.Sprintf("printdrop/%s/%s%s", userID, fileID, ext)

	extStart := time.Now()
	log.WithFields(logrus.Fields{
		"op":     "PresignPutObject",
		"bucket": s.bucket,
		"key":    objectKey,
	}).Info("s3 call start")

	presignResult, err := s.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		ContentType:   aws.String(fileType),
		ContentLength: aws.Int64(fileSize),
	}, s3.WithPresignExpires(s.presignExpiry))

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "PresignPutObject",
			"duration_ms": time.Since(extStart).Milliseconds(),
		}).Error("s3 call failed")
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GeneratePresignedURL",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "PresignPutObject",
		"duration_ms": time.Since(extStart).Milliseconds(),
	}).Info("s3 call done")

	log.WithFields(logrus.Fields{
		"op":          "GeneratePresignedURL",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")

	return &PresignedUploadResult{
		FileID:           fileID,
		UploadURL:        presignResult.URL,
		ObjectKey:        objectKey,
		ExpiresInSeconds: int(s.presignExpiry.Seconds()),
		MaxFileSize:      maxFileSize,
	}, nil
}
