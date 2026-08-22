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
	"github.com/qcom/qcom/internal/metrics"
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
	refreshTokenRepo := repository.NewRefreshTokenRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	pageRepo := repository.NewPageRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	addressRepo := repository.NewAddressRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	darkstoreRepo := repository.NewDarkstoreRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	etaCacheRepo := repository.NewETACacheRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	geocodeCacheRepo := repository.NewGeocodeCacheRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	deRepo := repository.NewDERepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	deStatusEventRepo := repository.NewDEStatusEventRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	referralRepo := repository.NewReferralRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	qrRepo := repository.NewQRRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	payoutConfigRepo := repository.NewPayoutConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	assignmentConfigRepo := repository.NewAssignmentConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	tripRepo := repository.NewTripRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	earningsLedgerRepo := repository.NewEarningsLedgerRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	disbursementRepo := repository.NewDisbursementRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	inKindDisbRepo := repository.NewInKindDisbursementRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	cronLockRepo := repository.NewCronLockRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	cashConfigRepo := repository.NewCashConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	cashDepositLedgerRepo := repository.NewCashDepositLedgerRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	deviceTokenRepo := repository.NewDeviceTokenRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	uploadUseCaseRepo := repository.NewUploadUseCaseRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	voiceProvisionRepo := repository.NewVoiceProvisionRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	callRecordRepo := repository.NewCallRecordRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	voiceCallContextRepo := repository.NewVoiceCallContextRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	ruleRepo := repository.NewRuleRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	adminUserRepo := repository.NewAdminUserRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	otpRepo := repository.NewOTPRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	smsOTPRoutingConfigRepo := repository.NewSMSOTPRoutingConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	tripReachedConfigRepo := repository.NewTripReachedConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)

	// Initialize services
	jwtService, err := service.NewJWTService(&cfg.JWT, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize JWT service")
	}

	twilioVerifyService := service.NewTwilioVerifyService(&cfg.Twilio, logger)
	africaTalkingSMS := service.NewAfricaTalkingSMSService(&cfg.AfricaTalking, logger)
	otpService := service.NewOTPService(
		twilioVerifyService,
		africaTalkingSMS,
		otpRepo,
		smsOTPRoutingConfigRepo,
		&cfg.OTP,
		logger,
	)
	refreshTokenService := service.NewRefreshTokenService(refreshTokenRepo, logger)
	addressService := service.NewAddressService(addressRepo, logger)
	googleGeocoder := service.NewGoogleGeocoder(cfg.Google.MapsAPIKey, logger)
	geocoder := service.NewCachedGeocoder(googleGeocoder, geocodeCacheRepo, logger)
	etaService := service.NewETAService(etaCacheRepo, cfg.Google.MapsAPIKey, logger)
	serviceabilityService := service.NewServiceabilityService(darkstoreRepo, addressService, geocoder, etaService, logger, cfg.IsTest, cfg.Serviceability.BypassUserIDs)
	qrService := service.NewQRService(logger)
	marketingQRService := service.NewMarketingQRService(qrRepo, logger)
	referralService := service.NewReferralService(referralRepo, deRepo, payoutConfigRepo, logger)
	deService := service.NewDEService(deRepo, qrService, referralService, earningsLedgerRepo, cashConfigRepo, darkstoreRepo, deStatusEventRepo, logger)
	presenceService := service.NewPresenceService(deStatusEventRepo, logger)
	cashDepositService := service.NewCashDepositService(deRepo, cashConfigRepo, logger)

	javaOrderClient := service.NewJavaOrderClient(cfg.Java.OrderServiceURL, logger)
	payoutService := service.NewPayoutService(payoutConfigRepo, earningsLedgerRepo, deRepo, tripRepo, referralService, logger)
	distanceService := service.NewDistanceService(cfg.Google.MapsAPIKey, logger)
	notificationService := service.NewNotificationService(&cfg.Firebase, deviceTokenRepo, logger)
	tripService := service.NewTripService(tripRepo, deRepo, javaOrderClient, payoutService, notificationService, deStatusEventRepo, logger)
	adminService := service.NewAdminService(tripRepo, deRepo, cashConfigRepo, deStatusEventRepo, notificationService, logger)
	appCtx, appCancel := context.WithCancel(context.Background())
	ruleCache := service.NewRuleCache(ruleRepo, 60*time.Second, logger)
	ruleCache.Start(appCtx)
	fareEngine := service.NewFareEngine(ruleCache)
	rewardCron := service.NewRewardCron(deRepo, tripRepo, ruleRepo, earningsLedgerRepo, cronLockRepo, logger)
	assignmentCron := service.NewAssignmentCron(tripRepo, deRepo, cronLockRepo, payoutConfigRepo, assignmentConfigRepo, cashConfigRepo, darkstoreRepo, deStatusEventRepo, javaOrderClient, distanceService, fareEngine, notificationService, logger)

	if err := service.SeedDefaults(appCtx, ruleRepo); err != nil {
		logger.WithError(err).Fatal("Failed to seed default rules")
	}

	adminUserService := service.NewAdminUserService(adminUserRepo, logger)
	if err := adminUserService.Bootstrap(
		appCtx,
		os.Getenv("ADMIN_BOOTSTRAP_USERNAME"),
		os.Getenv("ADMIN_BOOTSTRAP_PASSWORD"),
		os.Getenv("ADMIN_BOOTSTRAP_NAME"),
	); err != nil {
		logger.WithError(err).Error("Failed to bootstrap initial admin user")
	}

	s3Client, err := initS3(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize S3")
	}
	uploadService := service.NewUploadService(
		s3.NewPresignClient(s3Client),
		uploadUseCaseRepo,
		time.Duration(cfg.S3.PresignExpirySeconds)*time.Second,
		cfg.S3.TripPhotosBucket,
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
	geocodeHandlers := handlers.NewGeocodeHandlers(googleGeocoder, logger)
	deHandlers := handlers.NewDEHandlers(deService, qrService, payoutConfigRepo, cashConfigRepo, logger)
	referralHandlers := handlers.NewReferralHandlers(referralService, logger)
	configHandlers := handlers.NewConfigHandlers(payoutConfigRepo, logger)
	tripHandlers := handlers.NewTripHandlers(tripService, uploadService, logger)
	adminHandlers := handlers.NewAdminHandlers(adminService, logger)
	adminRulesHandlers := handlers.NewAdminRulesHandlers(ruleRepo, logger)
	adminAuthHandlers := handlers.NewAdminAuthHandlers(adminUserService, jwtService, logger)
	trackHandlers := handlers.NewTrackHandlers(tripRepo, deRepo, javaOrderClient, logger)
	earningsHandlers := handlers.NewEarningsHandlers(earningsLedgerRepo, disbursementRepo, inKindDisbRepo, deRepo, logger)
	disbursementHandlers := handlers.NewDisbursementHandlers(disbursementRepo, deRepo, earningsLedgerRepo, logger)
	inKindDisbHandlers := handlers.NewInKindDisbursementHandlers(inKindDisbRepo, earningsLedgerRepo, deRepo, notificationService, logger)
	cashDepositHandlers := handlers.NewCashDepositHandlers(cashDepositService, logger)
	adminDriverHandlers := handlers.NewAdminDriverHandlers(
		deService,
		deRepo,
		tripService,
		tripRepo,
		payoutConfigRepo,
		cashConfigRepo,
		cashDepositLedgerRepo,
		uploadService,
		presenceService,
		earningsHandlers,
		referralHandlers,
		inKindDisbHandlers,
		cfg.S3.Bucket,
		logger,
	)
	adminStoreHandlers := handlers.NewAdminStoreHandlers(darkstoreRepo, logger)
	notificationHandlers := handlers.NewNotificationHandlers(notificationService, logger)
	webhookHandlers := handlers.NewWebhookHandlers(logger)
	qrHandlers := handlers.NewQRHandlers(marketingQRService, logger)

	voiceTokenSvc := service.NewVoiceTokenService(cfg.VonageVoice, logger)
	voiceProvisionSvc := service.NewVoiceProvisionService(
		voiceProvisionRepo,
		"https://api.nexmo.com",
		cfg.VonageVoice.AppID,
		cfg.VonageVoice.PrivateKeyB64,
		logger,
	)
	voiceHandlers := handlers.NewVoiceHandlers(voiceTokenSvc, voiceProvisionSvc, tripRepo, callRecordRepo, callRecordRepo, voiceCallContextRepo, cfg.VonageVoice.SignatureSecret, logger)

	disputeRepo := repository.NewDisputeRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	dispositionRepo := repository.NewDisputeDispositionRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
	disputeNotifier := service.NewLoggingDisputeNotifier(logger)
	disputeService := service.NewDisputeService(
		disputeRepo, dispositionRepo, javaOrderClient, tripRepo, disputeNotifier,
		cfg.Dispute.EligibleOrderStatuses,
		logger,
	)
	disputeHandlers := handlers.NewDisputeHandlers(disputeService, uploadService, logger)

	adminDisputeService := service.NewAdminDisputeService(disputeRepo, dispositionRepo, tripRepo, deRepo)
	adminDisputeHandlers := handlers.NewAdminDisputeHandlers(adminDisputeService, uploadService, logger)
	adminSMSOTPRoutingHandlers := handlers.NewAdminSMSOTPRoutingHandlers(smsOTPRoutingConfigRepo, logger)
	adminTripReachedHandlers := handlers.NewAdminTripReachedHandlers(tripReachedConfigRepo, logger)

	authMiddleware := middleware.NewAuthMiddleware(jwtService, logger)
	router := setupRouter(authHandlers, homeHandlers, uploadHandlers, addressHandlers, serviceabilityHandlers, geocodeHandlers, deHandlers, referralHandlers, configHandlers, tripHandlers, adminHandlers, adminRulesHandlers, adminAuthHandlers, adminDriverHandlers, adminStoreHandlers, adminSMSOTPRoutingHandlers, adminTripReachedHandlers, trackHandlers, earningsHandlers, disbursementHandlers, cashDepositHandlers, notificationHandlers, webhookHandlers, disputeHandlers, adminDisputeHandlers, voiceHandlers, qrHandlers, authMiddleware, logger)

	srv := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// Internal metrics server, bound to loopback only so /metrics is never
	// reachable via the ALB/public API. Grafana Alloy scrapes it locally.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	metricsAddr := "127.0.0.1:" + cfg.Server.MetricsPort
	metricsSrv := &http.Server{
		Addr:    metricsAddr,
		Handler: metricsMux,
	}

	go func() {
		logger.WithField("port", cfg.Server.Port).Info("Starting server")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("Server failed to start")
		}
	}()

	go func() {
		logger.WithField("addr", metricsAddr).Info("Starting metrics server")
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Error("Metrics server failed")
		}
	}()

	assignmentCron.Start()
	rewardCron.Start(appCtx)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")
	appCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger.Info("Stopping assignment cron...")
	assignmentCron.Stop()

	if err := metricsSrv.Shutdown(ctx); err != nil {
		logger.WithError(err).Error("Metrics server forced to shutdown")
	}

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
	geocodeHandlers *handlers.GeocodeHandlers,
	deHandlers *handlers.DEHandlers,
	referralHandlers *handlers.ReferralHandlers,
	configHandlers *handlers.ConfigHandlers,
	tripHandlers *handlers.TripHandlers,
	adminHandlers *handlers.AdminHandlers,
	adminRulesHandlers *handlers.AdminRulesHandlers,
	adminAuthHandlers *handlers.AdminAuthHandlers,
	adminDriverHandlers *handlers.AdminDriverHandlers,
	adminStoreHandlers *handlers.AdminStoreHandlers,
	adminSMSOTPRoutingHandlers *handlers.AdminSMSOTPRoutingHandlers,
	adminTripReachedHandlers *handlers.AdminTripReachedHandlers,
	trackHandlers *handlers.TrackHandlers,
	earningsHandlers *handlers.EarningsHandlers,
	disbursementHandlers *handlers.DisbursementHandlers,
	cashDepositHandlers *handlers.CashDepositHandlers,
	notificationHandlers *handlers.NotificationHandlers,
	webhookHandlers *handlers.WebhookHandlers,
	disputeHandlers *handlers.DisputeHandlers,
	adminDisputeHandlers *handlers.AdminDisputeHandlers,
	voiceHandlers *handlers.VoiceHandlers,
	qrHandlers *handlers.QRHandlers,
	authMiddleware *middleware.AuthMiddleware,
	logger *logrus.Logger,
) *mux.Router {
	router := mux.NewRouter()

	router.Use(middleware.CORSMiddleware)
	router.Use(middleware.TraceIDMiddleware)
	router.Use(middleware.LoggingMiddleware(logger))
	router.Use(middleware.MetricsMiddleware)

	webhooks := router.PathPrefix("/webhooks").Subrouter()
	webhooks.HandleFunc("/outbound-whatsapp-message-status", webhookHandlers.OutboundWhatsAppMessageStatus).Methods("POST", "OPTIONS")
	webhooks.HandleFunc("/inbound-whatsapp-message", webhookHandlers.InboundWhatsAppMessage).Methods("POST", "OPTIONS")
	// Vonage answer webhook — no auth middleware; Vonage calls this directly.
	webhooks.HandleFunc("/voice/answer", voiceHandlers.AnswerWebhook).Methods("POST", "OPTIONS")
	// Vonage event webhook — no auth middleware; verified via HS256 signature inside handler.
	webhooks.HandleFunc("/voice/event", voiceHandlers.EventWebhook).Methods("POST", "OPTIONS")

	router.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}).Methods("GET", "OPTIONS")

	// Public marketing QR redirect — no auth. Encodes device-aware app-download links.
	router.HandleFunc("/q/{slug}", qrHandlers.Redirect).Methods("GET", "OPTIONS")

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

	// Admin login (username/password) — public; returns a bearer token. Must be
	// registered before the /admin subrouter so it is not gated by admin auth.
	api.HandleFunc("/admin/login", adminAuthHandlers.Login).Methods("POST", "OPTIONS")

	// All /admin/* routes (and the ops disbursement/cash-deposit endpoints, now
	// namespaced under /admin) require a valid admin bearer token (entity_type
	// "admin"). Preflight OPTIONS is short-circuited by CORSMiddleware first.
	admin := api.PathPrefix("/admin").Subrouter()
	admin.Use(authMiddleware.RequireAdminAuth)

	// Admin account self + user management.
	admin.HandleFunc("/me", adminAuthHandlers.Me).Methods("GET", "OPTIONS")
	admin.HandleFunc("/users", adminAuthHandlers.ListUsers).Methods("GET", "OPTIONS")
	admin.HandleFunc("/users", adminAuthHandlers.CreateUser).Methods("POST", "OPTIONS")
	admin.HandleFunc("/users/{username}/password", adminAuthHandlers.ChangePassword).Methods("POST", "OPTIONS")

	admin.HandleFunc("/sms-otp-routing", adminSMSOTPRoutingHandlers.GetConfig).Methods("GET", "OPTIONS")
	admin.HandleFunc("/sms-otp-routing", adminSMSOTPRoutingHandlers.PutConfig).Methods("PUT", "OPTIONS")

	admin.HandleFunc("/config/drop-reached", adminTripReachedHandlers.GetConfig).Methods("GET", "OPTIONS")
	admin.HandleFunc("/config/drop-reached", adminTripReachedHandlers.PatchConfig).Methods("PATCH", "OPTIONS")

	admin.HandleFunc("/assign", adminHandlers.AssignOrder).Methods("POST", "OPTIONS")
	admin.HandleFunc("/trips/{trip_id}/reassign-candidates", adminHandlers.ReassignCandidates).Methods("GET", "OPTIONS")
	admin.HandleFunc("/trips/{trip_id}/reassign", adminHandlers.ReassignTrip).Methods("POST", "OPTIONS")

	// Driver onboarding: presign document upload, then create the driver.
	admin.HandleFunc("/uploads/url", adminDriverHandlers.PresignDriverDoc).Methods("POST", "OPTIONS")
	admin.HandleFunc("/drivers", adminDriverHandlers.CreateDriver).Methods("POST", "OPTIONS")
	// List drivers by assigned darkstore (or Unassigned), name-searchable.
	admin.HandleFunc("/drivers", adminDriverHandlers.ListDrivers).Methods("GET", "OPTIONS")

	// Darkstore onboarding + management: list, create, look up, partial-edit,
	// and activate/deactivate. Specific-before-generic ordering kept for
	// consistency with the /drivers/{phone}/... convention above (no actual
	// ambiguity here since mux matches by literal path segment + method).
	// GET /darkstores lists active stores (?all=true includes inactive).
	admin.HandleFunc("/darkstores", adminStoreHandlers.ListDarkstores).Methods("GET", "OPTIONS")
	admin.HandleFunc("/darkstores", adminStoreHandlers.CreateDarkstore).Methods("POST", "OPTIONS")
	admin.HandleFunc("/darkstores/{id}/activate", adminStoreHandlers.ActivateDarkstore).Methods("POST", "OPTIONS")
	admin.HandleFunc("/darkstores/{id}/deactivate", adminStoreHandlers.DeactivateDarkstore).Methods("POST", "OPTIONS")
	admin.HandleFunc("/darkstores/{id}", adminStoreHandlers.GetDarkstore).Methods("GET", "OPTIONS")
	admin.HandleFunc("/darkstores/{id}", adminStoreHandlers.UpdateDarkstore).Methods("PATCH", "OPTIONS")

	// Driver detail + sub-resources (specific paths before the generic /{phone}).
	admin.HandleFunc("/drivers/{phone}/earnings", adminDriverHandlers.GetDriverEarnings).Methods("GET", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/disbursements", adminDriverHandlers.GetDriverDisbursements).Methods("GET", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/inkind-disbursements", adminDriverHandlers.RecordInKindDisbursement).Methods("POST", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/inkind-disbursements", adminDriverHandlers.ListInKindDisbursements).Methods("GET", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/referrals", adminDriverHandlers.GetDriverReferrals).Methods("GET", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/cash-ledger", adminDriverHandlers.GetDriverCashLedger).Methods("GET", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/cash-collections", adminDriverHandlers.GetDriverCashCollections).Methods("GET", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/presence", adminDriverHandlers.GetDriverPresence).Methods("GET", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/trip/pickup/complete", adminDriverHandlers.AdminCompletePickup).Methods("POST", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/trip/drop/complete", adminDriverHandlers.AdminCompleteDrop).Methods("POST", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/trip", adminDriverHandlers.GetDriverTrip).Methods("GET", "OPTIONS")
	// Order-scoped drop complete for the order-detail "Mark Delivered" action,
	// which has the order id but not the driver's phone on hand.
	admin.HandleFunc("/orders/{orderId}/drop/complete", adminDriverHandlers.AdminCompleteDropByOrder).Methods("POST", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}/assigned-store", adminDriverHandlers.UpdateAssignedStore).Methods("PATCH", "OPTIONS")
	admin.HandleFunc("/drivers/{phone}", adminDriverHandlers.GetDriver).Methods("GET", "OPTIONS")

	// Ops cash-deposit + disbursement recording (now gated under /admin).
	admin.HandleFunc("/de/{phone}/cash-deposit", cashDepositHandlers.RecordCashDeposit).Methods("POST", "OPTIONS")
	admin.HandleFunc("/de/{deId}/disbursement", disbursementHandlers.RecordDisbursement).Methods("POST", "OPTIONS")

	adminRules := admin.PathPrefix("/rules").Subrouter()
	adminRules.HandleFunc("", adminRulesHandlers.ListRules).Methods("GET", "OPTIONS")
	adminRules.HandleFunc("", adminRulesHandlers.CreateRule).Methods("POST", "OPTIONS")
	adminRules.HandleFunc("/{id}/versions", adminRulesHandlers.ListRuleVersions).Methods("GET", "OPTIONS")
	adminRules.HandleFunc("/{id}", adminRulesHandlers.UpdateRule).Methods("PUT", "OPTIONS")
	adminRules.HandleFunc("/{id}", adminRulesHandlers.DeleteRule).Methods("DELETE", "OPTIONS")

	// Dispute review: summary route registered before /{id} so it isn't shadowed.
	admin.HandleFunc("/disputes/summary", adminDisputeHandlers.Summary).Methods("GET", "OPTIONS")
	admin.HandleFunc("/disputes", adminDisputeHandlers.List).Methods("GET", "OPTIONS")
	admin.HandleFunc("/disputes/{id}", adminDisputeHandlers.UpdateStatus).Methods("PATCH", "OPTIONS")
	admin.HandleFunc("/disputes/{id}", adminDisputeHandlers.Get).Methods("GET", "OPTIONS")

	// Dynamic QR marketing campaigns (admin).
	admin.HandleFunc("/qr/campaigns", qrHandlers.ListCampaigns).Methods("GET", "OPTIONS")
	admin.HandleFunc("/qr/campaigns", qrHandlers.CreateCampaign).Methods("POST", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}/analytics", qrHandlers.Analytics).Methods("GET", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}/placements", qrHandlers.AddPlacement).Methods("POST", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}/placements/{slug}", qrHandlers.UpdatePlacement).Methods("PATCH", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}", qrHandlers.GetCampaign).Methods("GET", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}", qrHandlers.UpdateCampaign).Methods("PATCH", "OPTIONS")

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
	protected.HandleFunc("/uploads/view-url", uploadHandlers.GetViewURL).Methods("GET", "OPTIONS")
	protected.HandleFunc("/print/files/upload-url", uploadHandlers.GeneratePrintUploadURL).Methods("POST", "OPTIONS")

	// Serviceability — Bearer token or guest (X-User-Category: guest)
	serviceability := api.PathPrefix("/").Subrouter()
	serviceability.Use(authMiddleware.RequireAuthOrGuest)
	serviceability.HandleFunc("/serviceability", serviceabilityHandlers.CheckServiceability).Methods("POST", "OPTIONS")
	serviceability.HandleFunc("/geocode/reverse", geocodeHandlers.ReverseGeocode).Methods("POST", "OPTIONS")

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
	tripRoutes.HandleFunc("/{tripId}/task/{taskId}/photo/presign",
		tripHandlers.PresignTaskPhoto).Methods("POST", "OPTIONS")

	internal := router.PathPrefix("/internal/v1").Subrouter()
	internal.HandleFunc("/notifications/send", notificationHandlers.SendNotification).Methods("POST", "OPTIONS")
	internal.HandleFunc("/trips/cancel-by-order", tripHandlers.CancelTripByOrder).Methods("POST", "OPTIONS")
	internal.HandleFunc("/trips/payment/update", tripHandlers.UpdateTripPaymentByOrder).Methods("POST", "OPTIONS")
	// Picker-locked, unauthenticated, service-to-service upload endpoints
	// (order-service proxies picker uploads; relies on network isolation).
	internal.HandleFunc("/uploads/url", uploadHandlers.GenerateInternalPickerUploadURL).Methods("POST", "OPTIONS")
	internal.HandleFunc("/uploads/view-url", uploadHandlers.GetInternalPickerViewURL).Methods("GET", "OPTIONS")

	// VoIP token endpoint — accepts both customer and DE JWTs (RequireAuth).
	voice := api.PathPrefix("/voice").Subrouter()
	voice.Use(authMiddleware.RequireAuth)
	voice.HandleFunc("/token", voiceHandlers.PostToken).Methods("POST", "OPTIONS")

	// Customer dispute endpoints (require customer auth).
	// Static routes (/dispositions, /by-order) registered before parameterised /{id}
	// so gorilla/mux match order does not shadow them.
	customer := api.PathPrefix("/").Subrouter()
	customer.Use(authMiddleware.RequireCustomerAuth)
	customer.HandleFunc("/disputes/dispositions", disputeHandlers.ListDispositions).Methods("GET", "OPTIONS")
	customer.HandleFunc("/disputes", disputeHandlers.CreateDispute).Methods("POST", "OPTIONS")
	customer.HandleFunc("/disputes/by-order", disputeHandlers.GetDisputeByOrder).Methods("GET", "OPTIONS")
	customer.HandleFunc("/disputes/{id}", disputeHandlers.GetDispute).Methods("GET", "OPTIONS")

	return router
}
