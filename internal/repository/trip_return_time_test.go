package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

func testTripRepo(t *testing.T, handler http.HandlerFunc) *TripRepository {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := dynamodb.New(dynamodb.Options{
		Region:                          "us-east-1",
		Credentials:                     credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		BaseEndpoint:                    aws.String(srv.URL),
		HTTPClient:                      srv.Client(),
		RetryMaxAttempts:                1,
		DisableValidateResponseChecksum: true,
	})
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewTripRepository(client, "test-table", logger)
}

func captureBody(body *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*body = string(raw)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(`{}`))
	}
}

func TestStampReturnedAt_WritesOnlyIfUnset(t *testing.T) {
	var body string
	repo := testTripRepo(t, captureBody(&body))

	stamped, err := repo.StampReturnedAt(context.Background(), "T1", "2026-11-06T08:30:00Z")
	if err != nil || !stamped {
		t.Fatalf("StampReturnedAt = %v, %v; want true, nil", stamped, err)
	}
	for _, want := range []string{"TRIP!T1", "returned_at = :ret", "attribute_not_exists(returned_at)", "attribute_exists(PK)", "2026-11-06T08:30:00Z"} {
		if !strings.Contains(body, want) {
			t.Fatalf("request missing %q: %s", want, body)
		}
	}
}

func TestStampReturnedAt_AlreadyStampedIsNotAnError(t *testing.T) {
	repo := testTripRepo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Header().Set("X-Amzn-Errortype", "ConditionalCheckFailedException")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}`))
	})

	stamped, err := repo.StampReturnedAt(context.Background(), "T1", "2026-11-06T08:30:00Z")
	if err != nil || stamped {
		t.Fatalf("StampReturnedAt = %v, %v; want false, nil", stamped, err)
	}
}

func TestCompleteTripAndFreeDE_RecordsLastTripID(t *testing.T) {
	var body string
	repo := testTripRepo(t, captureBody(&body))

	if err := repo.CompleteTripAndFreeDE(context.Background(), "T1", "+260971000001", "221", nil, 0); err != nil {
		t.Fatalf("CompleteTripAndFreeDE: %v", err)
	}
	if !strings.Contains(body, "last_trip_id = :tid") {
		t.Fatalf("DE update must set last_trip_id: %s", body)
	}
}

func TestReassign_IncomingRiderClearsLastTripID(t *testing.T) {
	var body string
	repo := testTripRepo(t, captureBody(&body))

	if err := repo.Reassign(context.Background(), "T2", "+260A", "DE-A", "DE-B", "+260B", "ORD-2", "221", false, models.TripReassignment{}); err != nil {
		t.Fatalf("Reassign: %v", err)
	}
	if !strings.Contains(body, "REMOVE duty_index_key, scan_deadline_at, last_trip_id") {
		t.Fatalf("incoming rider must drop a stale last_trip_id: %s", body)
	}
}

func TestMarkEligibleFromScan_ClearsLastTripID(t *testing.T) {
	var body string
	repo := testDERepo(t, captureBody(&body))

	if err := repo.MarkEligibleFromScan(context.Background(), "+260971000001", "221", 1, 2, "2026-11-06T08:30:00Z"); err != nil {
		t.Fatalf("MarkEligibleFromScan: %v", err)
	}
	if !strings.Contains(body, "last_trip_id") || !strings.Contains(body, "REMOVE") {
		t.Fatalf("scan must REMOVE last_trip_id: %s", body)
	}
}
