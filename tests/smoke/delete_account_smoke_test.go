//go:build smoke

package smoke

import (
	"net/http"
	"testing"
)

// TestSmoke_DeleteAccount exercises the App Store account-deletion flow against
// a live API: a customer deletes their account, their refresh token stops
// working, and re-logging in with the same number mints a brand-new userId.
func TestSmoke_DeleteAccount(t *testing.T) {
	phone := smokePhone()

	auth := authenticateCustomer(t, phone)
	if auth.EntityType != "customer" {
		t.Fatalf("expected entity_type=customer, got %q", auth.EntityType)
	}
	if auth.EntityID == "" {
		t.Fatal("empty user_id after auth")
	}

	// Delete the account.
	resp, body := do(t, "DELETE", "/api/v1/users/me", bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %v", resp.StatusCode, body)
	}

	// The refresh token must no longer mint access tokens.
	resp2, _ := do(t, "POST", "/api/v1/auth/refresh", nil, map[string]interface{}{
		"refresh_token": auth.RefreshToken,
	})
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after delete: expected 401, got %d", resp2.StatusCode)
	}

	// Re-login with the same number yields a fresh userId.
	reAuth := authenticateCustomer(t, phone)
	if reAuth.EntityID == "" {
		t.Fatal("empty user_id after re-login")
	}
	if reAuth.EntityID == auth.EntityID {
		t.Fatalf("re-login should mint a new userId; got same id %q", reAuth.EntityID)
	}

	// Cleanup: remove the freshly created account too.
	do(t, "DELETE", "/api/v1/users/me", bearer(reAuth.AccessToken), nil)
}

// TestSmoke_DeleteAccount_RejectsOtherUserID verifies the live endpoint refuses
// to delete an account other than the caller's own (IDOR guard).
func TestSmoke_DeleteAccount_RejectsOtherUserID(t *testing.T) {
	phone := smokePhone()
	auth := authenticateCustomer(t, phone)

	resp, body := do(t, "DELETE", "/api/v1/users/me", bearer(auth.AccessToken), map[string]interface{}{
		"user_id": "not-my-user-id",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for mismatched user_id, got %d: %v", resp.StatusCode, body)
	}

	// Cleanup with a correct request.
	do(t, "DELETE", "/api/v1/users/me", bearer(auth.AccessToken), nil)
}
