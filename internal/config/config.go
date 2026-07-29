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
	Server         ServerConfig
	DynamoDB       DynamoDBConfig
	JWT            JWTConfig
	OTP            OTPConfig
	S3             S3Config
	Google         GoogleConfig
	Java           JavaConfig
	Vonage         VonageConfig
	Firebase       FirebaseConfig
	Dispute        DisputeConfig
	VonageVoice    VoiceConfig
	Serviceability ServiceabilityConfig
	IsTest         bool
}

type ServerConfig struct {
	Port string
	// AllowedOrigins is the CORS origin allowlist (CORS_ALLOWED_ORIGINS,
	// comma-separated). Empty falls back to the wildcard "*".
	AllowedOrigins []string
	// InternalAPIKey, when non-empty, is required in the X-Internal-Api-Key
	// header on every /internal/v1 route. Empty keeps those routes open and
	// relying solely on network isolation.
	InternalAPIKey string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	// MetricsPort is the port for the internal, localhost-only Prometheus
	// metrics server (/metrics). It is intentionally separate from Port so the
	// public ALB never routes to it.
	MetricsPort string
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
	// MasterBypassCode, when non-empty, is accepted as a valid OTP for every
	// phone number. It exists for smoke/QA environments only and MUST stay
	// unset in production, where an empty value disables the bypass entirely.
	MasterBypassCode string
}

type S3Config struct {
	Endpoint             string
	Region               string
	Bucket               string
	TripPhotosBucket     string
	PresignExpirySeconds int
	ForcePathStyle       bool
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

type VoiceConfig struct {
	AppID           string
	PrivateKeyB64   string
	SignatureSecret string
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

type ServiceabilityConfig struct {
	// BypassUserIDs is the allowlist of JWT entity_ids for which the polygon
	// serviceability check is skipped and a fixed dummy store is returned.
	// Populated from SERVICEABILITY_BYPASS_USER_IDS (comma-separated); empty
	// disables the feature. Matching is case-sensitive, so entries are NOT
	// uppercased (unlike DISPUTE_ELIGIBLE_ORDER_STATUSES).
	BypassUserIDs []string
}

func loadVoiceConfig() VoiceConfig {
	return VoiceConfig{
		AppID:           getEnv("VONAGE_VOICE_APP_ID", ""),
		PrivateKeyB64:   getEnv("VONAGE_VOICE_PRIVATE_KEY", ""),
		SignatureSecret: getEnv("VONAGE_VOICE_SIGNATURE_SECRET", ""),
	}
}

func Load() (*Config, error) {
	s3Bucket := getEnv("S3_BUCKET", "printdrop-documents")
	cfg := &Config{
		Server: ServerConfig{
			Port:           getEnv("PORT", "8080"),
			ReadTimeout:    15 * time.Second,
			WriteTimeout:   15 * time.Second,
			MetricsPort:    getEnv("METRICS_PORT", "2112"),
			AllowedOrigins: splitAndTrimPreserveCase(getEnv("CORS_ALLOWED_ORIGINS", "")),
			InternalAPIKey: getEnv("INTERNAL_API_KEY", ""),
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
			Length:           getEnvAsInt("OTP_LENGTH", 6),
			Expiry:           getEnvAsDuration("OTP_EXPIRY", 10*time.Minute),
			MaxAttempts:      getEnvAsInt("OTP_MAX_ATTEMPTS", 5),
			MasterBypassCode: getEnv("OTP_MASTER_BYPASS_CODE", ""),
		},
		S3: S3Config{
			Endpoint:             getEnv("S3_ENDPOINT", ""),
			Region:               getEnv("S3_REGION", "ap-southeast-2"),
			Bucket:               s3Bucket,
			TripPhotosBucket:     getEnv("S3_TRIP_PHOTOS_BUCKET", s3Bucket),
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
			// Dispute store attribution (DisputeService.resolveStoreID) resolves the
			// store from the disputed order's Trip, which is only safe because every
			// route to DELIVERED goes through a trip. Widening this list to a
			// pre-dispatch status will silently start producing disputes stamped
			// StoreID=UNKNOWN, with no error anywhere.
			EligibleOrderStatuses: splitAndTrim(getEnv("DISPUTE_ELIGIBLE_ORDER_STATUSES", "DELIVERED")),
		},
		VonageVoice: loadVoiceConfig(),
		Serviceability: ServiceabilityConfig{
			BypassUserIDs: splitAndTrimPreserveCase(getEnv("SERVICEABILITY_BYPASS_USER_IDS", "")),
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

// splitAndTrimPreserveCase splits a comma-separated string, trims whitespace
// from each element, and drops empty entries WITHOUT changing case. Used for
// values matched verbatim (e.g. JWT entity_ids) where uppercasing would break
// the comparison.
func splitAndTrimPreserveCase(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
