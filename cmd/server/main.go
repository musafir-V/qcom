package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/handlers"
	"github.com/qcom/qcom/internal/middleware"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
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

	// Initialize repositories
	userRepo := repository.NewUserRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	otpRepo := repository.NewOTPRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	refreshTokenRepo := repository.NewRefreshTokenRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	pageRepo := repository.NewPageRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	addressRepo := repository.NewAddressRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	darkstoreRepo := repository.NewDarkstoreRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	etaCacheRepo := repository.NewETACacheRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	geocodeCacheRepo := repository.NewGeocodeCacheRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	deRepo := repository.NewDERepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	referralRepo := repository.NewReferralRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	payoutConfigRepo := repository.NewPayoutConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	assignmentConfigRepo := repository.NewAssignmentConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	tripRepo := repository.NewTripRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	earningsLedgerRepo := repository.NewEarningsLedgerRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	weeklySummaryRepo := repository.NewWeeklySummaryRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	disbursementRepo := repository.NewDisbursementRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	cronLockRepo := repository.NewCronLockRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	vonageJWTRepo := repository.NewVonageJWTRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	cashConfigRepo := repository.NewCashConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	deviceTokenRepo := repository.NewDeviceTokenRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	uploadUseCaseRepo := repository.NewUploadUseCaseRepository(dynamoClient, cfg.DynamoDB.TableName, logger)

	// Initialize services
	jwtService, err := service.NewJWTService(&cfg.JWT, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize JWT service")
	}

	vonageService := service.NewVonageService(&cfg.Vonage, vonageJWTRepo, logger)
	otpService := service.NewOTPService(otpRepo, vonageService, &cfg.OTP, logger)
	refreshTokenService := service.NewRefreshTokenService(refreshTokenRepo, logger)
	addressService := service.NewAddressService(addressRepo, logger)
	geocoder := service.NewCachedGeocoder(
		service.NewGoogleGeocoder(cfg.Google.MapsAPIKey, logger),
		geocodeCacheRepo,
		logger,
	)
	etaService := service.NewETAService(etaCacheRepo, cfg.Google.MapsAPIKey, logger)
	serviceabilityService := service.NewServiceabilityService(darkstoreRepo, addressService, geocoder, etaService, logger, cfg.IsTest)
	qrService := service.NewQRService(logger)
	referralService := service.NewReferralService(referralRepo, deRepo, payoutConfigRepo, logger)
	deService := service.NewDEService(deRepo, qrService, referralService, earningsLedgerRepo, cashConfigRepo, logger)
	cashDepositService := service.NewCashDepositService(deRepo, cashConfigRepo, logger)

	javaOrderClient := service.NewJavaOrderClient(cfg.Java.OrderServiceURL, logger)
	payoutService := service.NewPayoutService(payoutConfigRepo, earningsLedgerRepo, deRepo, tripRepo, referralService, logger)
	distanceService := service.NewDistanceService(cfg.Google.MapsAPIKey, logger)
	notificationService := service.NewNotificationService(&cfg.Firebase, deviceTokenRepo, logger)
	tripService := service.NewTripService(tripRepo, deRepo, javaOrderClient, payoutService, notificationService, logger)
	adminService := service.NewAdminService(tripRepo, deRepo, logger)
	assignmentCron := service.NewAssignmentCron(tripRepo, deRepo, cronLockRepo, payoutConfigRepo, assignmentConfigRepo, cashConfigRepo, darkstoreRepo, javaOrderClient, distanceService, notificationService, logger)
	weeklyBonusCron := service.NewWeeklyBonusCron(deRepo, tripRepo, weeklySummaryRepo, earningsLedgerRepo, payoutConfigRepo, cronLockRepo, logger)

	s3Client, err := initS3(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize S3")
	}
	uploadService := service.NewUploadService(
		s3.NewPresignClient(s3Client),
		uploadUseCaseRepo,
		time.Duration(cfg.S3.PresignExpirySeconds)*time.Second,
		logger,
	)

	authHandlers := handlers.NewAuthHandlers(
		otpService,
		jwtService,
		refreshTokenService,
		userRepo,
		deRepo,
		logger,
	)

	homeHandlers := handlers.NewHomeHandlers(pageRepo, logger)
	uploadHandlers := handlers.NewUploadHandlers(uploadService, logger)
	addressHandlers := handlers.NewAddressHandlers(addressService, logger)
	serviceabilityHandlers := handlers.NewServiceabilityHandlers(serviceabilityService, logger)
	deHandlers := handlers.NewDEHandlers(deService, qrService, payoutConfigRepo, cashConfigRepo, logger)
	referralHandlers := handlers.NewReferralHandlers(referralService, logger)
	configHandlers := handlers.NewConfigHandlers(payoutConfigRepo, logger)
	tripHandlers := handlers.NewTripHandlers(tripService, logger)
	adminHandlers := handlers.NewAdminHandlers(adminService, logger)
	trackHandlers := handlers.NewTrackHandlers(tripRepo, deRepo, javaOrderClient, logger)
	earningsHandlers := handlers.NewEarningsHandlers(earningsLedgerRepo, disbursementRepo, deRepo, logger)
	disbursementHandlers := handlers.NewDisbursementHandlers(disbursementRepo, deRepo, logger)
	cashDepositHandlers := handlers.NewCashDepositHandlers(cashDepositService, logger)
	notificationHandlers := handlers.NewNotificationHandlers(notificationService, logger)
	webhookHandlers := handlers.NewWebhookHandlers(logger)

	authMiddleware := middleware.NewAuthMiddleware(jwtService, logger)
	router := setupRouter(authHandlers, homeHandlers, uploadHandlers, addressHandlers, serviceabilityHandlers, deHandlers, referralHandlers, configHandlers, tripHandlers, adminHandlers, trackHandlers, earningsHandlers, disbursementHandlers, cashDepositHandlers, notificationHandlers, webhookHandlers, authMiddleware, logger)

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

	assignmentCron.Start()
	weeklyBonusCron.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger.Info("Stopping assignment cron...")
	assignmentCron.Stop()

	logger.Info("Stopping weekly bonus cron...")
	weeklyBonusCron.Stop()

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
		awsCfg, err = awsconfig.LoadDefaultConfig(context.TODO(), awsconfig.WithRegion(cfg.DynamoDB.Region))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := dynamodb.NewFromConfig(awsCfg)
	logger.Info("DynamoDB client initialized")
	return client, nil
}

func initS3(cfg *config.Config, logger *logrus.Logger) (*s3.Client, error) {
	var opts []func(*s3.Options)

	if cfg.S3.Endpoint != "" {
		awsCfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
			awsconfig.WithRegion(cfg.S3.Region),
			awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("dummy", "dummy", "")),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to load AWS config for S3: %w", err)
		}
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.S3.Endpoint)
			o.UsePathStyle = cfg.S3.ForcePathStyle
		})
		client := s3.NewFromConfig(awsCfg, opts...)
		logger.WithField("endpoint", cfg.S3.Endpoint).Info("S3 client initialized (custom endpoint)")
		return client, nil
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.TODO(), awsconfig.WithRegion(cfg.S3.Region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config for S3: %w", err)
	}
	client := s3.NewFromConfig(awsCfg)
	logger.Info("S3 client initialized")
	return client, nil
}

func setupRouter(
	authHandlers *handlers.AuthHandlers,
	homeHandlers *handlers.HomeHandlers,
	uploadHandlers *handlers.UploadHandlers,
	addressHandlers *handlers.AddressHandlers,
	serviceabilityHandlers *handlers.ServiceabilityHandlers,
	deHandlers *handlers.DEHandlers,
	referralHandlers *handlers.ReferralHandlers,
	configHandlers *handlers.ConfigHandlers,
	tripHandlers *handlers.TripHandlers,
	adminHandlers *handlers.AdminHandlers,
	trackHandlers *handlers.TrackHandlers,
	earningsHandlers *handlers.EarningsHandlers,
	disbursementHandlers *handlers.DisbursementHandlers,
	cashDepositHandlers *handlers.CashDepositHandlers,
	notificationHandlers *handlers.NotificationHandlers,
	webhookHandlers *handlers.WebhookHandlers,
	authMiddleware *middleware.AuthMiddleware,
	logger *logrus.Logger,
) *mux.Router {
	router := mux.NewRouter()

	router.Use(middleware.CORSMiddleware)
	router.Use(middleware.TraceIDMiddleware)
	router.Use(middleware.LoggingMiddleware(logger))

	webhooks := router.PathPrefix("/webhooks").Subrouter()
	webhooks.HandleFunc("/outbound-whatsapp-message-status", webhookHandlers.OutboundWhatsAppMessageStatus).Methods("POST", "OPTIONS")
	webhooks.HandleFunc("/inbound-whatsapp-message", webhookHandlers.InboundWhatsAppMessage).Methods("POST", "OPTIONS")

	router.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}).Methods("GET", "OPTIONS")

	api := router.PathPrefix("/api/v1").Subrouter()

	// Auth endpoints (no auth required)
	auth := api.PathPrefix("/auth").Subrouter()
	auth.HandleFunc("/initiate-otp", authHandlers.InitiateOTP).Methods("POST", "OPTIONS")
	auth.HandleFunc("/verify-otp", authHandlers.VerifyOTP).Methods("POST", "OPTIONS")
	auth.HandleFunc("/refresh", authHandlers.RefreshToken).Methods("POST", "OPTIONS")
	// Logout requires a valid access token to identify the session; the handler
	// reads JWT claims from context, so it must sit behind RequireAuth.
	authProtected := auth.PathPrefix("").Subrouter()
	authProtected.Use(authMiddleware.RequireAuth)
	authProtected.HandleFunc("/logout", authHandlers.Logout).Methods("POST", "OPTIONS")

	// DE onboarding (no auth required)
	api.HandleFunc("/de/register", deHandlers.Register).Methods("POST", "OPTIONS")

	// QR code display endpoint (no auth required — shown on darkstore screen)
	api.HandleFunc("/stores/{storeId}/qr", deHandlers.GetStoreQR).Methods("GET", "OPTIONS")

	// Payout config update endpoint (no auth — ops/runtime tuning)
	api.HandleFunc("/config/payout", configHandlers.UpdatePayoutConfig).Methods("PATCH", "OPTIONS")

	admin := api.PathPrefix("/admin").Subrouter()
	admin.HandleFunc("/assign", adminHandlers.AssignOrder).Methods("POST", "OPTIONS")

	// Ops disbursement recording endpoint (no auth — internal)
	api.HandleFunc("/de/{deId}/disbursement", disbursementHandlers.RecordDisbursement).Methods("POST", "OPTIONS")
	api.HandleFunc("/admin/de/{phone}/cash-deposit", cashDepositHandlers.RecordCashDeposit).Methods("POST", "OPTIONS")

	// Protected customer endpoints
	protected := api.PathPrefix("/").Subrouter()
	protected.Use(authMiddleware.RequireAuth)
	protected.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		phone := r.Context().Value("phone").(string)
		entityID := r.Context().Value("entity_id").(string)
		entityType := r.Context().Value("entity_type").(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"entity_id":"%s","entity_type":"%s","phone":"%s"}`, entityID, entityType, phone)))
	}).Methods("GET")
	// Account deletion (App Store Guideline 5.1.1(v)) — deletes the caller's own account.
	protected.HandleFunc("/users/me", authHandlers.DeleteAccount).Methods("DELETE", "OPTIONS")
	protected.HandleFunc("/home", homeHandlers.GetHome).Methods("POST", "OPTIONS")
	protected.HandleFunc("/uploads/url", uploadHandlers.GenerateUploadURL).Methods("POST", "OPTIONS")
	protected.HandleFunc("/print/files/upload-url", uploadHandlers.GeneratePrintUploadURL).Methods("POST", "OPTIONS")

	// Serviceability — Bearer token or guest (X-User-Category: guest)
	serviceability := api.PathPrefix("/").Subrouter()
	serviceability.Use(authMiddleware.RequireAuthOrGuest)
	serviceability.HandleFunc("/serviceability", serviceabilityHandlers.CheckServiceability).Methods("POST", "OPTIONS")

	// Address endpoints — specific routes must be registered before the parameterized /:id route
	protected.HandleFunc("/addresses/suggest", addressHandlers.GetSuggestedAddresses).Methods("GET")
	protected.HandleFunc("/addresses", addressHandlers.GetMyAddresses).Methods("GET")
	protected.HandleFunc("/addresses", addressHandlers.CreateAddress).Methods("POST")
	protected.HandleFunc("/addresses/{id}", addressHandlers.GetAddressByID).Methods("GET")
	protected.HandleFunc("/addresses/{id}", addressHandlers.UpdateReceiverDetails).Methods("PATCH")
	protected.HandleFunc("/addresses/{id}", addressHandlers.RemoveAddress).Methods("DELETE")

	// Customer order tracking
	protected.HandleFunc("/orders/{orderId}/track", trackHandlers.Track).Methods("GET", "OPTIONS")
	protected.HandleFunc("/device-token", notificationHandlers.PutDeviceToken).Methods("PUT", "OPTIONS")

	// DE duty endpoints (require DE auth)
	deProtected := api.PathPrefix("/de").Subrouter()
	deProtected.Use(authMiddleware.RequireDEAuth)
	deProtected.HandleFunc("/me", deHandlers.GetMe).Methods("GET", "OPTIONS")
	deProtected.HandleFunc("/duty/start", deHandlers.StartDuty).Methods("POST", "OPTIONS")
	deProtected.HandleFunc("/duty/end", deHandlers.EndDuty).Methods("POST", "OPTIONS")
	deProtected.HandleFunc("/trip", tripHandlers.GetCurrentTrip).Methods("GET", "OPTIONS")
	deProtected.HandleFunc("/referral", referralHandlers.GetReferralScreen).Methods("GET", "OPTIONS")
	deProtected.HandleFunc("/earnings/summary", earningsHandlers.GetEarningsSummary).Methods("GET", "OPTIONS")
	deProtected.HandleFunc("/earnings/disbursements", earningsHandlers.GetDisbursements).Methods("GET", "OPTIONS")

	// Trip progression endpoints (require DE auth)
	tripRoutes := api.PathPrefix("/trip").Subrouter()
	tripRoutes.Use(authMiddleware.RequireDEAuth)
	tripRoutes.HandleFunc("/{tripId}/task/{taskId}/status/update",
		tripHandlers.UpdateTaskStatus).Methods("POST", "OPTIONS")
	tripRoutes.HandleFunc("/{tripId}/accept", tripHandlers.AcceptTrip).Methods("POST", "OPTIONS")
	tripRoutes.HandleFunc("/{tripId}/reject", tripHandlers.RejectTrip).Methods("POST", "OPTIONS")
	tripRoutes.HandleFunc("/{tripId}/verify-pickup", tripHandlers.VerifyPickup).Methods("POST", "OPTIONS")

	internal := router.PathPrefix("/internal/v1").Subrouter()
	internal.HandleFunc("/notifications/send", notificationHandlers.SendNotification).Methods("POST", "OPTIONS")

	return router
}
