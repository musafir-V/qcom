//go:build smoke

package smoke

import (
	"net/http"
	"testing"
)

// TestSmoke_Logout verifies logout succeeds with a valid access token and
// revokes the refresh token so it can no longer mint new access tokens.
func TestSmoke_Logout(t *testing.T) {
	phone := smokePhone()
	auth := authenticateCustomer(t, phone)

	resp, body := do(t, "POST", "/api/v1/auth/logout", bearer(auth.AccessToken), map[string]interface{}{
		"refresh_token": auth.RefreshToken,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: expected 200, got %d: %v", resp.StatusCode, body)
	}
	msg, _ := body["message"].(string)
	if msg != "Logged out successfully" {
		t.Fatalf("logout: unexpected message %q", msg)
	}

	resp2, _ := do(t, "POST", "/api/v1/auth/refresh", nil, map[string]interface{}{
		"refresh_token": auth.RefreshToken,
	})
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: expected 401, got %d", resp2.StatusCode)
	}
}

// TestSmoke_Logout_RequiresAuth ensures logout without a Bearer token is rejected.
func TestSmoke_Logout_RequiresAuth(t *testing.T) {
	resp, body := do(t, "POST", "/api/v1/auth/logout", nil, map[string]interface{}{
		"refresh_token": "dummy",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout without auth: expected 401, got %d: %v", resp.StatusCode, body)
	}
}
