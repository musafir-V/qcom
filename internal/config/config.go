package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Server   ServerConfig
	DynamoDB DynamoDBConfig
	Redis    RedisConfig
	JWT      JWTConfig
	OTP      OTPConfig
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

type RedisConfig struct {
	Endpoint string
	Password string
	DB       int
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
		Redis: RedisConfig{
			Endpoint: getEnv("REDIS_ENDPOINT", "localhost:6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvAsInt("REDIS_DB", 0),
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
	}

	if cfg.JWT.SecretKey == "" {
		return nil, fmt.Errorf("JWT_SECRET_KEY environment variable is required")
	}

	if len(cfg.JWT.SecretKey) < 32 {
		return nil, fmt.Errorf("JWT_SECRET_KEY must be at least 32 bytes (256 bits)")
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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
