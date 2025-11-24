package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/handlers"
	"github.com/qcom/qcom/internal/middleware"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.InfoLevel)

	cfg, err := config.Load()
	if err != nil {
		logger.WithError(err).Fatal("Failed to load configuration")
	}

	dynamoClient, err := initDynamoDB(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize DynamoDB")
	}

	redisClient, err := initRedis(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize Redis")
	}
	defer redisClient.Close()

	userRepo := repository.NewUserRepository(dynamoClient, cfg.DynamoDB.TableName, logger)

	jwtService, err := service.NewJWTService(&cfg.JWT, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize JWT service")
	}

	otpService := service.NewOTPService(redisClient, &cfg.OTP, logger)
	refreshTokenService := service.NewRefreshTokenService(redisClient, logger)

	authHandlers := handlers.NewAuthHandlers(
		otpService,
		jwtService,
		refreshTokenService,
		userRepo,
		logger,
	)

	authMiddleware := middleware.NewAuthMiddleware(jwtService, logger)
	router := setupRouter(authHandlers, authMiddleware, logger)

	srv := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	go func() {
		logger.WithField("port", cfg.Server.Port).Info("Starting server")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("Server failed to start")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.WithError(err).Fatal("Server forced to shutdown")
	}

	logger.Info("Server exited")
}

func initDynamoDB(cfg *config.Config, logger *logrus.Logger) (*dynamodb.Client, error) {
	var awsCfg aws.Config
	var err error

	if cfg.DynamoDB.Endpoint != "" {
		awsCfg, err = awsconfig.LoadDefaultConfig(context.TODO(),
			awsconfig.WithRegion(cfg.DynamoDB.Region),
			awsconfig.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
				func(service, region string, options ...interface{}) (aws.Endpoint, error) {
					return aws.Endpoint{
						URL:           cfg.DynamoDB.Endpoint,
						SigningRegion: cfg.DynamoDB.Region,
					}, nil
				})),
		)
	} else {
		awsCfg, err = awsconfig.LoadDefaultConfig(context.TODO())
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := dynamodb.NewFromConfig(awsCfg)
	logger.Info("DynamoDB client initialized")
	return client, nil
}

func initRedis(cfg *config.Config, logger *logrus.Logger) (*redis.Client, error) {
	var tlsConfig *tls.Config

	// Enable TLS if configured
	if cfg.Redis.UseTLS {
		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		logger.Info("TLS enabled for Redis connection")
	}

	// Create Redis client with password authentication
	client := redis.NewClient(&redis.Options{
		Addr:      cfg.Redis.Endpoint,
		Password:  cfg.Redis.Password,
		DB:        cfg.Redis.DB,
		TLSConfig: tlsConfig,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pong, err := client.Ping(ctx).Result()
	if err != nil {
		logger.WithError(err).Warn("Failed to connect to Redis, continuing anyway")
		return client, nil
	}

	logger.WithFields(logrus.Fields{
		"ping_response": pong,
		"endpoint":      cfg.Redis.Endpoint,
		"tls_enabled":   cfg.Redis.UseTLS,
	}).Info("Redis client initialized successfully")

	return client, nil
}

func setupRouter(
	authHandlers *handlers.AuthHandlers,
	authMiddleware *middleware.AuthMiddleware,
	logger *logrus.Logger,
) *mux.Router {
	router := mux.NewRouter()

	router.Use(middleware.CORSMiddleware)
	router.Use(middleware.LoggingMiddleware(logger))

	router.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}).Methods("GET", "OPTIONS")

	api := router.PathPrefix("/api/v1").Subrouter()

	auth := api.PathPrefix("/auth").Subrouter()
	auth.HandleFunc("/initiate-otp", authHandlers.InitiateOTP).Methods("POST", "OPTIONS")
	auth.HandleFunc("/verify-otp", authHandlers.VerifyOTP).Methods("POST", "OPTIONS")
	auth.HandleFunc("/refresh", authHandlers.RefreshToken).Methods("POST", "OPTIONS")
	auth.HandleFunc("/logout", authHandlers.Logout).Methods("POST", "OPTIONS")

	protected := api.PathPrefix("/").Subrouter()
	protected.Use(authMiddleware.RequireAuth)
	protected.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		phone := r.Context().Value("phone").(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"phone":"%s"}`, phone)))
	}).Methods("GET")

	return router
}
