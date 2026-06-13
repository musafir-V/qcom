//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// userRowExists reports whether the USER!<phone> / METADATA item is present.
func userRowExists(t *testing.T, phone string) bool {
	t.Helper()
	result, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "USER!" + phone},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		t.Fatalf("get user row: %v", err)
	}
	return result.Item != nil
}

// refreshAccessToken calls /auth/refresh and returns the HTTP status.
func refreshAccessToken(t *testing.T, refreshToken string) int {
	t.Helper()
	resp, _ := doRequest(t, "POST", "/api/v1/auth/refresh", nil, map[string]interface{}{
		"refresh_token": refreshToken,
	})
	return resp.StatusCode
}

// TestDeleteAccount_HappyPath covers the full App Store deletion flow:
// delete removes the user row + revokes tokens, and re-login mints a new userId.
func TestDeleteAccount_HappyPath(t *testing.T) {
	phone := "+919870000001"
	deleteStoredOTP(t, phone)

	auth := authenticateUser(t, phone)
	if auth.EntityID == "" {
		t.Fatal("expected a user_id after auth")
	}
	if !userRowExists(t, phone) {
		t.Fatal("user row should exist after auth")
	}

	resp, body := doRequest(t, "DELETE", "/api/v1/users/me", bearerHeaders(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %v", resp.StatusCode, body)
	}

	// User row is gone.
	if userRowExists(t, phone) {
		t.Fatal("user row should be deleted")
	}

	// Refresh token is revoked — cannot mint new access tokens.
	if code := refreshAccessToken(t, auth.RefreshToken); code != http.StatusUnauthorized {
		t.Fatalf("refresh after delete: expected 401, got %d", code)
	}

	// Re-login with the same number mints a brand-new userId.
	deleteStoredOTP(t, phone)
	reAuth := authenticateUser(t, phone)
	if reAuth.EntityID == "" {
		t.Fatal("expected a user_id after re-login")
	}
	if reAuth.EntityID == auth.EntityID {
		t.Fatalf("re-login should mint a new userId; got same id %q", reAuth.EntityID)
	}

	// cleanup
	doRequest(t, "DELETE", "/api/v1/users/me", bearerHeaders(reAuth.AccessToken), nil)
	deleteStoredOTP(t, phone)
}

// TestDeleteAccount_Idempotent verifies a second delete still returns 200.
func TestDeleteAccount_Idempotent(t *testing.T) {
	phone := "+919870000002"
	deleteStoredOTP(t, phone)
	auth := authenticateUser(t, phone)

	resp, _ := doRequest(t, "DELETE", "/api/v1/users/me", bearerHeaders(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first delete: expected 200, got %d", resp.StatusCode)
	}

	// Access token is stateless (15m TTL) so it still authenticates; a repeat
	// delete must succeed idempotently even though the row is already gone.
	resp2, body2 := doRequest(t, "DELETE", "/api/v1/users/me", bearerHeaders(auth.AccessToken), nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second delete: expected 200, got %d: %v", resp2.StatusCode, body2)
	}

	deleteStoredOTP(t, phone)
}

// TestDeleteAccount_RejectsOtherUserID guards against IDOR: a body user_id that
// is not the caller's own is rejected and nothing is deleted.
func TestDeleteAccount_RejectsOtherUserID(t *testing.T) {
	phone := "+919870000003"
	deleteStoredOTP(t, phone)
	auth := authenticateUser(t, phone)

	resp, body := doRequest(t, "DELETE", "/api/v1/users/me", bearerHeaders(auth.AccessToken),
		map[string]interface{}{"user_id": "someone-elses-id"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for mismatched user_id, got %d: %v", resp.StatusCode, body)
	}
	if !userRowExists(t, phone) {
		t.Fatal("account must not be deleted on a rejected request")
	}

	// Matching user_id is accepted.
	resp2, _ := doRequest(t, "DELETE", "/api/v1/users/me", bearerHeaders(auth.AccessToken),
		map[string]interface{}{"user_id": auth.EntityID})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for own user_id, got %d", resp2.StatusCode)
	}

	deleteStoredOTP(t, phone)
}

// TestDeleteAccount_ForbiddenForDE ensures delivery executives cannot self-delete
// through this customer endpoint.
func TestDeleteAccount_ForbiddenForDE(t *testing.T) {
	phone := "+919870000004"
	deleteStoredOTP(t, phone)
	if resp, body := doRequest(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number":       phone,
		"name":               "Test DE",
		"profile_url":        "https://example.com/photo.jpg",
		"nrc_url":            "https://example.com/nrc.jpg",
		"driver_license_url": "https://example.com/license.jpg",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("de/register: expected 201, got %d: %v", resp.StatusCode, body)
	}
	auth := authenticateDE(t, phone)

	resp, body := doRequest(t, "DELETE", "/api/v1/users/me", bearerHeaders(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("DE delete: expected 403, got %d: %v", resp.StatusCode, body)
	}

	deleteStoredOTP(t, phone)
}
