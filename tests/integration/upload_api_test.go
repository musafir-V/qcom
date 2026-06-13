//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/handlers"
	"github.com/qcom/qcom/internal/middleware"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

const (
	testTableName    = "QComTestTable"
	testPhone        = "+1234567890"
	dynamoDBEndpoint = "http://localhost:8000"
	s3Endpoint       = "http://localhost:4566"
	s3Bucket         = "printdrop-documents"
	s3Region         = "ap-southeast-2"
	dynamoDBRegion   = "us-east-1"
)

var (
	testServer   *httptest.Server
	dynamoClient *dynamodb.Client
	s3Client     *s3.Client
)

func TestMain(m *testing.M) {
	if err := startDockerContainers(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start docker containers: %v\n", err)
		os.Exit(1)
	}

	if err := waitForServices(); err != nil {
		fmt.Fprintf(os.Stderr, "Services not ready: %v\n", err)
		stopDockerContainers()
		os.Exit(1)
	}

	if err := setupInfra(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to setup infra: %v\n", err)
		stopDockerContainers()
		os.Exit(1)
	}

	server, err := setupServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to setup server: %v\n", err)
		stopDockerContainers()
		os.Exit(1)
	}
	testServer = server

	code := m.Run()

	testServer.Close()
	stopDockerContainers()
	os.Exit(code)
}

func startDockerContainers() error {
	fmt.Println("Starting Docker containers...")

	// Stop any existing containers
	exec.Command("docker", "rm", "-f", "qcom-test-dynamodb", "qcom-test-localstack").Run()

	// Start DynamoDB
	cmd := exec.Command("docker", "run", "-d",
		"--name", "qcom-test-dynamodb",
		"-p", "8000:8000",
		"-e", "AWS_ACCESS_KEY_ID=dummy",
		"-e", "AWS_SECRET_ACCESS_KEY=dummy",
		"-e", "AWS_DEFAULT_REGION=us-east-1",
		"amazon/dynamodb-local:2.0.0",
		"-jar", "DynamoDBLocal.jar", "-sharedDb", "-inMemory",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start DynamoDB: %w\n%s", err, string(out))
	}

	// Start LocalStack for S3
	cmd = exec.Command("docker", "run", "-d",
		"--name", "qcom-test-localstack",
		"-p", "4566:4566",
		"-e", "SERVICES=s3",
		"-e", "DEFAULT_REGION=ap-southeast-2",
		"-e", "AWS_ACCESS_KEY_ID=dummy",
		"-e", "AWS_SECRET_ACCESS_KEY=dummy",
		"localstack/localstack:3.0",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start LocalStack: %w\n%s", err, string(out))
	}

	fmt.Println("Docker containers started")
	return nil
}

func stopDockerContainers() {
	fmt.Println("Stopping Docker containers...")
	exec.Command("docker", "rm", "-f", "qcom-test-dynamodb", "qcom-test-localstack").Run()
}

func waitForServices() error {
	fmt.Println("Waiting for services...")

	// Wait for DynamoDB
	for i := 0; i < 30; i++ {
		resp, err := http.Get(dynamoDBEndpoint)
		if err == nil {
			resp.Body.Close()
			fmt.Println("DynamoDB is ready")
			break
		}
		if i == 29 {
			return fmt.Errorf("DynamoDB did not become ready in time")
		}
		time.Sleep(1 * time.Second)
	}

	// Wait for LocalStack
	for i := 0; i < 30; i++ {
		resp, err := http.Get(s3Endpoint + "/_localstack/health")
		if err == nil {
			resp.Body.Close()
			fmt.Println("LocalStack is ready")
			break
		}
		if i == 29 {
			return fmt.Errorf("LocalStack did not become ready in time")
		}
		time.Sleep(1 * time.Second)
	}

	return nil
}

func setupInfra() error {
	ctx := context.TODO()

	// Initialize DynamoDB client
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(dynamoDBRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("dummy", "dummy", "")),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	dynamoClient = dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(dynamoDBEndpoint)
	})

	// Create DynamoDB table with UserIdIndex GSI for address queries
	_, err = dynamoClient.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(testTableName),
		AttributeDefinitions: []dynamodbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: dynamodbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: dynamodbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("user_id"), AttributeType: dynamodbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("created_at"), AttributeType: dynamodbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("referral_code"), AttributeType: dynamodbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []dynamodbtypes.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: dynamodbtypes.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: dynamodbtypes.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []dynamodbtypes.GlobalSecondaryIndex{
			{
				IndexName: aws.String("UserIdIndex"),
				KeySchema: []dynamodbtypes.KeySchemaElement{
					{AttributeName: aws.String("user_id"), KeyType: dynamodbtypes.KeyTypeHash},
					{AttributeName: aws.String("created_at"), KeyType: dynamodbtypes.KeyTypeRange},
				},
				Projection: &dynamodbtypes.Projection{
					ProjectionType: dynamodbtypes.ProjectionTypeAll,
				},
			},
			{
				IndexName: aws.String("ReferralCodeIndex"),
				KeySchema: []dynamodbtypes.KeySchemaElement{
					{AttributeName: aws.String("referral_code"), KeyType: dynamodbtypes.KeyTypeHash},
				},
				Projection: &dynamodbtypes.Projection{
					ProjectionType: dynamodbtypes.ProjectionTypeAll,
				},
			},
		},
		BillingMode: dynamodbtypes.BillingModePayPerRequest,
	})
	if err != nil && !strings.Contains(err.Error(), "Table already exists") {
		return fmt.Errorf("failed to create DynamoDB table: %w", err)
	}
	fmt.Println("DynamoDB table created with UserIdIndex GSI")

	// Initialize S3 client
	s3Cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(s3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("dummy", "dummy", "")),
	)
	if err != nil {
		return fmt.Errorf("failed to load S3 AWS config: %w", err)
	}

	s3Client = s3.NewFromConfig(s3Cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s3Endpoint)
		o.UsePathStyle = true
	})

	// Create S3 bucket (non-us-east-1 requires LocationConstraint)
	_, err = s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s3Bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraintApSoutheast2,
		},
	})
	if err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
		return fmt.Errorf("failed to create S3 bucket: %w", err)
	}
	fmt.Println("S3 bucket created")

	return nil
}

func setupServer() (*httptest.Server, error) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.TextFormatter{ForceColors: true})

	cfg := &config.Config{
		Server: config.ServerConfig{
			Port: "0",
		},
		DynamoDB: config.DynamoDBConfig{
			Endpoint:  dynamoDBEndpoint,
			Region:    dynamoDBRegion,
			TableName: testTableName,
		},
		JWT: config.JWTConfig{
			SecretKey:     "test-secret-key-that-is-at-least-32-bytes-long!!",
			AccessExpiry:  15 * time.Minute,
			RefreshExpiry: 7 * 24 * time.Hour,
		},
		OTP: config.OTPConfig{
			Length:      6,
			Expiry:      10 * time.Minute,
			MaxAttempts: 5,
		},
		S3: config.S3Config{
			Endpoint:             s3Endpoint,
			Region:               s3Region,
			Bucket:               s3Bucket,
			PresignExpirySeconds: 300,
			ForcePathStyle:       true,
		},
	}

	// Build DynamoDB client for repositories
	awsCfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion(cfg.DynamoDB.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("dummy", "dummy", "")),
	)
	if err != nil {
		return nil, err
	}
	dynamo := dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(cfg.DynamoDB.Endpoint)
	})

	// Build S3 client
	s3Cfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion(cfg.S3.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("dummy", "dummy", "")),
	)
	if err != nil {
		return nil, err
	}
	s3c := s3.NewFromConfig(s3Cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.S3.Endpoint)
		o.UsePathStyle = cfg.S3.ForcePathStyle
	})

	// Repositories
	userRepo := repository.NewUserRepository(dynamo, cfg.DynamoDB.TableName, logger)
	otpRepo := repository.NewOTPRepository(dynamo, cfg.DynamoDB.TableName, logger)
	refreshTokenRepo := repository.NewRefreshTokenRepository(dynamo, cfg.DynamoDB.TableName, logger)
	pageRepo := repository.NewPageRepository(dynamo, cfg.DynamoDB.TableName, logger)
	addressRepo := repository.NewAddressRepository(dynamo, cfg.DynamoDB.TableName, logger)
	darkstoreRepo := repository.NewDarkstoreRepository(dynamo, cfg.DynamoDB.TableName, logger)
	deRepo := repository.NewDERepository(dynamo, cfg.DynamoDB.TableName, logger)
	referralRepo := repository.NewReferralRepository(dynamo, cfg.DynamoDB.TableName, logger)
	payoutConfigRepo := repository.NewPayoutConfigRepository(dynamo, cfg.DynamoDB.TableName, logger)
	earningsLedgerRepo := repository.NewEarningsLedgerRepository(dynamo, cfg.DynamoDB.TableName, logger)
	disbursementRepo := repository.NewDisbursementRepository(dynamo, cfg.DynamoDB.TableName, logger)

	// Services
	jwtService, err := service.NewJWTService(&cfg.JWT, logger)
	if err != nil {
		return nil, err
	}

	vonageJWTRepo := repository.NewVonageJWTRepository(dynamo, cfg.DynamoDB.TableName, logger)
	vonageMockServer := newSuccessVonageMockServer()
	vonageService := service.NewVonageService(&config.VonageConfig{
		AppID:         testVonageAppID,
		PrivateKeyB64: testVonagePrivateKeyB64(),
		WhatsAppFrom:  testVonageWhatsAppFrom,
	}, vonageJWTRepo, logger)
	vonageService.SetMessagesURL(vonageMockServer.URL)
	vonageService.SetHTTPClient(vonageMockServer.Client())

	otpService := service.NewOTPService(otpRepo, vonageService, &cfg.OTP, logger)
	refreshTokenService := service.NewRefreshTokenService(refreshTokenRepo, logger)
	uploadService := service.NewUploadService(s3c, &cfg.S3, logger)
	addressService := service.NewAddressService(addressRepo, logger)
	serviceabilityService := service.NewServiceabilityService(darkstoreRepo, addressService, testGeocoder, testETAService, logger, false)
	qrService := service.NewQRService(logger)
	referralService := service.NewReferralService(referralRepo, deRepo, payoutConfigRepo, logger)
	deService := service.NewDEService(deRepo, qrService, referralService, earningsLedgerRepo, logger)

	// Handlers
	authHandlers := handlers.NewAuthHandlers(otpService, jwtService, refreshTokenService, userRepo, deRepo, logger)
	homeHandlers := handlers.NewHomeHandlers(pageRepo, logger)
	uploadHandlers := handlers.NewUploadHandlers(uploadService, logger)
	addressHandlers := handlers.NewAddressHandlers(addressService, logger)
	serviceabilityHandlers := handlers.NewServiceabilityHandlers(serviceabilityService, logger)
	deHandlers := handlers.NewDEHandlers(deService, qrService, payoutConfigRepo, logger)
	referralHandlers := handlers.NewReferralHandlers(referralService, logger)
	configHandlers := handlers.NewConfigHandlers(payoutConfigRepo, logger)
	earningsHandlers := handlers.NewEarningsHandlers(earningsLedgerRepo, disbursementRepo, deRepo, logger)
	disbursementHandlers := handlers.NewDisbursementHandlers(disbursementRepo, deRepo, logger)

	// Middleware
	authMiddleware := middleware.NewAuthMiddleware(jwtService, logger)

	// Router
	router := buildRouter(authHandlers, homeHandlers, uploadHandlers, addressHandlers, serviceabilityHandlers, deHandlers, referralHandlers, configHandlers, earningsHandlers, disbursementHandlers, authMiddleware, logger)
	server := httptest.NewServer(router)

	fmt.Printf("Test server started at %s\n", server.URL)
	return server, nil
}

func buildRouter(
	authHandlers *handlers.AuthHandlers,
	homeHandlers *handlers.HomeHandlers,
	uploadHandlers *handlers.UploadHandlers,
	addressHandlers *handlers.AddressHandlers,
	serviceabilityHandlers *handlers.ServiceabilityHandlers,
	deHandlers *handlers.DEHandlers,
	referralHandlers *handlers.ReferralHandlers,
	configHandlers *handlers.ConfigHandlers,
	earningsHandlers *handlers.EarningsHandlers,
	disbursementHandlers *handlers.DisbursementHandlers,
	authMiddleware *middleware.AuthMiddleware,
	logger *logrus.Logger,
) *mux.Router {
	router := mux.NewRouter()
	router.Use(middleware.CORSMiddleware)

	router.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}).Methods("GET")

	api := router.PathPrefix("/api/v1").Subrouter()

	auth := api.PathPrefix("/auth").Subrouter()
	auth.HandleFunc("/initiate-otp", authHandlers.InitiateOTP).Methods("POST")
	auth.HandleFunc("/verify-otp", authHandlers.VerifyOTP).Methods("POST")
	auth.HandleFunc("/refresh", authHandlers.RefreshToken).Methods("POST")

	// DE onboarding (no auth)
	api.HandleFunc("/de/register", deHandlers.Register).Methods("POST")

	// QR display (no auth)
	api.HandleFunc("/stores/{storeId}/qr", deHandlers.GetStoreQR).Methods("GET")

	// Payout config update (no auth)
	api.HandleFunc("/config/payout", configHandlers.UpdatePayoutConfig).Methods("PATCH")

	// Ops disbursement recording (no auth)
	api.HandleFunc("/de/{deId}/disbursement", disbursementHandlers.RecordDisbursement).Methods("POST")

	protected := api.PathPrefix("/").Subrouter()
	protected.Use(authMiddleware.RequireAuth)
	protected.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		phone := r.Context().Value("phone").(string)
		entityID := r.Context().Value("entity_id").(string)
		entityType := r.Context().Value("entity_type").(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"entity_id":"%s","entity_type":"%s","phone":"%s"}`, entityID, entityType, phone)
	}).Methods("GET")
	protected.HandleFunc("/users/me", authHandlers.DeleteAccount).Methods("DELETE")
	protected.HandleFunc("/home", homeHandlers.GetHome).Methods("POST")
	protected.HandleFunc("/serviceability", serviceabilityHandlers.CheckServiceability).Methods("POST")
	protected.HandleFunc("/print/files/upload-url", uploadHandlers.GenerateUploadURL).Methods("POST")

	protected.HandleFunc("/addresses/suggest", addressHandlers.GetSuggestedAddresses).Methods("GET")
	protected.HandleFunc("/addresses", addressHandlers.GetMyAddresses).Methods("GET")
	protected.HandleFunc("/addresses", addressHandlers.CreateAddress).Methods("POST")
	protected.HandleFunc("/addresses/{id}", addressHandlers.GetAddressByID).Methods("GET")
	protected.HandleFunc("/addresses/{id}", addressHandlers.UpdateReceiverDetails).Methods("PATCH")
	protected.HandleFunc("/addresses/{id}", addressHandlers.RemoveAddress).Methods("DELETE")

	// DE protected endpoints
	deProtected := api.PathPrefix("/de").Subrouter()
	deProtected.Use(authMiddleware.RequireDEAuth)
	deProtected.HandleFunc("/me", deHandlers.GetMe).Methods("GET")
	deProtected.HandleFunc("/duty/start", deHandlers.StartDuty).Methods("POST")
	deProtected.HandleFunc("/referral", referralHandlers.GetReferralScreen).Methods("GET")
	deProtected.HandleFunc("/earnings/summary", earningsHandlers.GetEarningsSummary).Methods("GET")
	deProtected.HandleFunc("/earnings/disbursements", earningsHandlers.GetDisbursements).Methods("GET")

	return router
}

// ---------- helpers ----------

func getTestOTP(phone string) (string, error) {
	result, err := dynamoClient.GetItem(context.TODO(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: fmt.Sprintf("OTP_TEST#%s", phone)},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to get test OTP: %w", err)
	}
	if result.Item == nil {
		return "", fmt.Errorf("test OTP not found")
	}
	otpAttr, ok := result.Item["OTP"].(*dynamodbtypes.AttributeValueMemberS)
	if !ok {
		return "", fmt.Errorf("OTP attribute not found or wrong type")
	}
	return otpAttr.Value, nil
}

type authTokens struct {
	AccessToken  string
	RefreshToken string
	EntityID     string
	EntityType   string
}

func authenticateUser(t *testing.T, phone string) authTokens {
	t.Helper()

	// 1. Initiate OTP
	body := fmt.Sprintf(`{"phone_number":"%s"}`, phone)
	resp, err := http.Post(testServer.URL+"/api/v1/auth/initiate-otp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("initiate-otp request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initiate-otp returned %d", resp.StatusCode)
	}

	// 2. Read test OTP from DynamoDB
	otp, err := getTestOTP(phone)
	if err != nil {
		t.Fatalf("failed to read test OTP: %v", err)
	}

	// 3. Verify OTP (no X-App-Type header → customer)
	return doVerifyOTP(t, phone, otp, "")
}

// doVerifyOTP sends verify-otp and returns the parsed tokens.
// appType: "de" sets X-App-Type: de header; "" means customer (no header).
func doVerifyOTP(t *testing.T, phone, otp, appType string) authTokens {
	t.Helper()

	body := fmt.Sprintf(`{"phone_number":"%s","otp":"%s"}`, phone, otp)
	req, _ := http.NewRequest("POST", testServer.URL+"/api/v1/auth/verify-otp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if appType == "de" {
		req.Header.Set("X-App-Type", "de")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("verify-otp request failed: %v", err)
	}
	defer resp.Body.Close()

	var verifyResp struct {
		AccessToken  string                 `json:"access_token"`
		RefreshToken string                 `json:"refresh_token"`
		EntityType   string                 `json:"entity_type"`
		Entity       map[string]interface{} `json:"entity"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&verifyResp); err != nil {
		t.Fatalf("failed to decode verify-otp response: %v", err)
	}
	if verifyResp.AccessToken == "" {
		t.Fatalf("verify-otp did not return access token (status %d)", resp.StatusCode)
	}

	// Extract entity ID — field name differs by entity type
	entityID := ""
	if verifyResp.Entity != nil {
		if id, ok := verifyResp.Entity["user_id"].(string); ok {
			entityID = id
		} else if id, ok := verifyResp.Entity["de_id"].(string); ok {
			entityID = id
		}
	}

	return authTokens{
		AccessToken:  verifyResp.AccessToken,
		RefreshToken: verifyResp.RefreshToken,
		EntityID:     entityID,
		EntityType:   verifyResp.EntityType,
	}
}

func doUploadURLRequest(t *testing.T, token string, reqBody map[string]interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()

	b, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", testServer.URL+"/api/v1/print/files/upload-url", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload-url request failed: %v", err)
	}

	var result map[string]interface{}
	respBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(respBytes, &result)

	return resp, result
}

// ---------- tests ----------

func TestHealthCheck(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/health")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestUploadURL_Success(t *testing.T) {
	auth := authenticateUser(t, testPhone)

	resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "assignment.pdf",
		"file_type": "application/pdf",
		"file_size": 23456789,
	})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	// Verify response fields
	fileID, ok := result["file_id"].(string)
	if !ok || fileID == "" {
		t.Fatal("response missing file_id")
	}

	uploadURL, ok := result["upload_url"].(string)
	if !ok || uploadURL == "" {
		t.Fatal("response missing upload_url")
	}

	objectKey, ok := result["object_key"].(string)
	if !ok || objectKey == "" {
		t.Fatal("response missing object_key")
	}

	expectedPrefix := fmt.Sprintf("printdrop/%s/%s", auth.EntityID, fileID)
	if !strings.HasPrefix(objectKey, expectedPrefix) {
		t.Fatalf("object_key %q should start with %q", objectKey, expectedPrefix)
	}
	if !strings.HasSuffix(objectKey, ".pdf") {
		t.Fatalf("object_key %q should end with .pdf", objectKey)
	}

	expiresIn, ok := result["expires_in_seconds"].(float64)
	if !ok || expiresIn != 300 {
		t.Fatalf("expected expires_in_seconds=300, got %v", expiresIn)
	}

	maxSize, ok := result["max_file_size"].(float64)
	if !ok || maxSize != 50*1024*1024 {
		t.Fatalf("expected max_file_size=%d, got %v", 50*1024*1024, maxSize)
	}

	t.Logf("file_id=%s object_key=%s", fileID, objectKey)
}

func TestUploadURL_ActualUpload(t *testing.T) {
	auth := authenticateUser(t, "+1111111111")

	resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "test.pdf",
		"file_type": "application/pdf",
		"file_size": 11,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	uploadURL := result["upload_url"].(string)
	objectKey := result["object_key"].(string)

	// The presigned URL points to localstack; replace the hostname
	// so the test can reach it from the host network.
	uploadURL = strings.Replace(uploadURL, "localhost.localstack.cloud", "localhost", 1)

	fileContent := []byte("hello world")

	req, _ := http.NewRequest("PUT", uploadURL, bytes.NewReader(fileContent))
	req.Header.Set("Content-Type", "application/pdf")
	req.ContentLength = int64(len(fileContent))

	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT to presigned URL failed: %v", err)
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(putResp.Body)
		t.Fatalf("PUT returned %d: %s", putResp.StatusCode, string(b))
	}

	// Verify the object exists in S3
	headResp, err := s3Client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(s3Bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		t.Fatalf("HeadObject failed (file not uploaded?): %v", err)
	}
	if *headResp.ContentLength != int64(len(fileContent)) {
		t.Fatalf("expected content length %d, got %d", len(fileContent), *headResp.ContentLength)
	}

	t.Log("File uploaded and verified in S3")
}

func TestUploadURL_NoAuth(t *testing.T) {
	resp, _ := doUploadURLRequest(t, "", map[string]interface{}{
		"file_name": "test.pdf",
		"file_type": "application/pdf",
		"file_size": 1000,
	})

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestUploadURL_InvalidToken(t *testing.T) {
	resp, _ := doUploadURLRequest(t, "invalid.jwt.token", map[string]interface{}{
		"file_name": "test.pdf",
		"file_type": "application/pdf",
		"file_size": 1000,
	})

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestUploadURL_FileTooLarge(t *testing.T) {
	auth := authenticateUser(t, "+1222222222")

	resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "huge.pdf",
		"file_type": "application/pdf",
		"file_size": 51 * 1024 * 1024, // 51 MB
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}

	errObj, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error in response")
	}
	if errObj["code"] != "VALIDATION_FAILED" {
		t.Fatalf("expected VALIDATION_FAILED, got %v", errObj["code"])
	}
}

func TestUploadURL_InvalidFileType(t *testing.T) {
	auth := authenticateUser(t, "+1333333333")

	resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "malware.exe",
		"file_type": "application/x-executable",
		"file_size": 1000,
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}

	errObj, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error in response")
	}
	if errObj["code"] != "VALIDATION_FAILED" {
		t.Fatalf("expected VALIDATION_FAILED, got %v", errObj["code"])
	}
}

func TestUploadURL_MissingFileName(t *testing.T) {
	auth := authenticateUser(t, "+1444444444")

	resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_type": "application/pdf",
		"file_size": 1000,
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
}

func TestUploadURL_ZeroFileSize(t *testing.T) {
	auth := authenticateUser(t, "+1555555555")

	resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "empty.pdf",
		"file_type": "application/pdf",
		"file_size": 0,
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
}

func TestUploadURL_MimeTypeExtensionMismatch(t *testing.T) {
	auth := authenticateUser(t, "+1666666666")

	// Valid MIME but disallowed extension
	resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "photo.exe",
		"file_type": "image/png",
		"file_size": 1000,
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
}

func TestUploadURL_AllAllowedTypes(t *testing.T) {
	auth := authenticateUser(t, "+1777777777")

	cases := []struct {
		fileName string
		fileType string
	}{
		{"doc.pdf", "application/pdf"},
		{"doc.doc", "application/msword"},
		{"doc.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"photo.jpg", "image/jpeg"},
		{"photo.png", "image/png"},
		{"photo.heic", "image/heic"},
	}

	for _, tc := range cases {
		t.Run(tc.fileName, func(t *testing.T) {
			resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
				"file_name": tc.fileName,
				"file_type": tc.fileType,
				"file_size": 5000,
			})

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 for %s/%s, got %d: %v", tc.fileName, tc.fileType, resp.StatusCode, result)
			}

			objectKey, _ := result["object_key"].(string)
			expectedExt := tc.fileName[strings.LastIndex(tc.fileName, "."):]
			if !strings.HasSuffix(objectKey, expectedExt) {
				t.Fatalf("object_key %q should end with %s", objectKey, expectedExt)
			}
		})
	}
}

func TestUploadURL_UserIDInObjectKey(t *testing.T) {
	auth := authenticateUser(t, "+1888888888")

	_, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "test.png",
		"file_type": "image/png",
		"file_size": 1000,
	})

	objectKey, _ := result["object_key"].(string)
	if !strings.Contains(objectKey, auth.EntityID) {
		t.Fatalf("object_key %q should contain user_id %s", objectKey, auth.EntityID)
	}

	parts := strings.Split(objectKey, "/")
	if len(parts) != 3 || parts[0] != "printdrop" || parts[1] != auth.EntityID {
		t.Fatalf("object_key %q should match printdrop/{user_id}/{file_id}.ext", objectKey)
	}
}

func TestUploadURL_ExactBoundaryFileSize(t *testing.T) {
	auth := authenticateUser(t, "+1999999990")

	// Exactly 50 MB should succeed
	resp, result := doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "max.pdf",
		"file_type": "application/pdf",
		"file_size": 50 * 1024 * 1024,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for exactly 50MB, got %d: %v", resp.StatusCode, result)
	}

	// 50 MB + 1 byte should fail
	resp, result = doUploadURLRequest(t, auth.AccessToken, map[string]interface{}{
		"file_name": "toobig.pdf",
		"file_type": "application/pdf",
		"file_size": 50*1024*1024 + 1,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for 50MB+1, got %d: %v", resp.StatusCode, result)
	}
}

func TestUploadURL_InvalidJSON(t *testing.T) {
	auth := authenticateUser(t, "+1999999991")

	req, _ := http.NewRequest("POST", testServer.URL+"/api/v1/print/files/upload-url",
		strings.NewReader("not valid json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
