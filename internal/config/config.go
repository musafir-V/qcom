package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server   ServerConfig
	DynamoDB DynamoDBConfig
	JWT      JWTConfig
	OTP      OTPConfig
	S3       S3Config
	Google   GoogleConfig
	Java     JavaConfig
	Vonage   VonageConfig
	Firebase FirebaseConfig
	Dispute  DisputeConfig
	IsTest   bool
}

type ServerConfig struct {
	Port         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

type DynamoDBConfig struct {
	Endpoint  string
	Region    string
	TableName string
}

type JWTConfig struct {
	SecretKey     string
	AccessExpiry  time.Duration
	RefreshExpiry time.Duration
}

type OTPConfig struct {
	Length      int
	Expiry      time.Duration
	MaxAttempts int
}

type S3Config struct {
	Endpoint         string
	Region           string
	Bucket           string
	PresignExpirySeconds int
	ForcePathStyle   bool
}

type GoogleConfig struct {
	MapsAPIKey string
}

type JavaConfig struct {
	OrderServiceURL string
}

type VonageConfig struct {
	AppID         string
	PrivateKeyB64 string
	WhatsAppFrom  string
}

type FirebaseConfig struct {
	// CredentialsB64 is the base64-encoded Firebase service-account JSON.
	// When empty or invalid, push notifications degrade to a logged no-op
	// (assignment + the app's foreground-service poll still work).
	CredentialsB64 string
}

type DisputeConfig struct {
	// EligibleOrderStatuses is the set of order statuses that allow a customer
	// to open a dispute. Populated from DISPUTE_ELIGIBLE_ORDER_STATUSES
	// (comma-separated); defaults to ["DELIVERED"].
	EligibleOrderStatuses []string
}

func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port:         getEnv("PORT", "8080"),
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
		},
		DynamoDB: DynamoDBConfig{
			Endpoint:  getEnv("DYNAMODB_ENDPOINT", ""),
			Region:    getEnv("DYNAMODB_REGION", "us-east-1"),
			TableName: getEnv("DYNAMODB_TABLE_NAME", "QComTable"),
		},
		JWT: JWTConfig{
			SecretKey:     getEnv("JWT_SECRET_KEY", ""),
			AccessExpiry:  getEnvAsDuration("JWT_ACCESS_EXPIRY", 15*time.Minute),
			RefreshExpiry: getEnvAsDuration("JWT_REFRESH_EXPIRY", 7*24*time.Hour),
		},
		OTP: OTPConfig{
			Length:      getEnvAsInt("OTP_LENGTH", 6),
			Expiry:      getEnvAsDuration("OTP_EXPIRY", 10*time.Minute),
			MaxAttempts: getEnvAsInt("OTP_MAX_ATTEMPTS", 5),
		},
		S3: S3Config{
			Endpoint:             getEnv("S3_ENDPOINT", ""),
			Region:               getEnv("S3_REGION", "ap-southeast-2"),
			Bucket:               getEnv("S3_BUCKET", "printdrop-documents"),
			PresignExpirySeconds: getEnvAsInt("S3_PRESIGN_EXPIRY_SECONDS", 300),
			ForcePathStyle:       getEnv("S3_FORCE_PATH_STYLE", "false") == "true",
		},
		Google: GoogleConfig{
			MapsAPIKey: getEnv("GOOGLE_MAPS_API_KEY", ""),
		},
		Java: JavaConfig{
			OrderServiceURL: getEnv("JAVA_ORDER_SERVICE_URL", "http://localhost:8081"),
		},
		Vonage: VonageConfig{
			AppID:         getEnv("VONAGE_APP_ID", ""),
			PrivateKeyB64: getEnv("VONAGE_PRIVATE_KEY", ""),
			WhatsAppFrom:  getEnv("VONAGE_WHATSAPP_FROM", ""),
		},
		Firebase: FirebaseConfig{
			CredentialsB64: getEnv("FIREBASE_CREDENTIALS_B64", ""),
		},
		Dispute: DisputeConfig{
			EligibleOrderStatuses: splitAndTrim(getEnv("DISPUTE_ELIGIBLE_ORDER_STATUSES", "DELIVERED")),
		},
		// IS_TEST (or IS_TRUE): skip polygon check; use first active darkstore from DDB.
		IsTest: envBool("IS_TEST") || envBool("IS_TRUE"),
	}

	if cfg.JWT.SecretKey == "" {
		return nil, fmt.Errorf("JWT_SECRET_KEY environment variable is required")
	}

	if len(cfg.JWT.SecretKey) < 32 {
		return nil, fmt.Errorf("JWT_SECRET_KEY must be at least 32 bytes (256 bits)")
	}

	if cfg.Vonage.AppID == "" {
		return nil, fmt.Errorf("VONAGE_APP_ID environment variable is required")
	}
	if cfg.Vonage.PrivateKeyB64 == "" {
		return nil, fmt.Errorf("VONAGE_PRIVATE_KEY environment variable is required")
	}
	if _, err := base64.StdEncoding.DecodeString(cfg.Vonage.PrivateKeyB64); err != nil {
		return nil, fmt.Errorf("VONAGE_PRIVATE_KEY must be valid base64: %w", err)
	}
	if cfg.Vonage.WhatsAppFrom == "" {
		return nil, fmt.Errorf("VONAGE_WHATSAPP_FROM environment variable is required")
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func envBool(key string) bool {
	return os.Getenv(key) == "true"
}

func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvAsDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

// splitAndTrim splits a comma-separated string, trims whitespace from each
// element, uppercases it, and drops empty entries.
func splitAndTrim(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, strings.ToUpper(t))
		}
	}
	return out
}
