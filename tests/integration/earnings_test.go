//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// TestEarningsSummary_Empty verifies a fresh DE with no completed trips sees a
// zero outstanding balance on the earnings summary endpoint.
func TestEarningsSummary_Empty(t *testing.T) {
	phone := uniquePhone("90")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	resp, result := doRequest(t, "GET", "/api/v1/de/earnings/summary", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	if bal, ok := result["outstanding_balance_zmw"].(float64); !ok || bal != 0 {
		t.Fatalf("expected outstanding_balance_zmw 0, got %v", result["outstanding_balance_zmw"])
	}
}

// TestEarningsDisbursements_Empty verifies a fresh DE returns an empty (non-error)
// disbursement list.
func TestEarningsDisbursements_Empty(t *testing.T) {
	phone := uniquePhone("91")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	resp, result := doRequest(t, "GET", "/api/v1/de/earnings/disbursements", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	if _, ok := result["disbursements"]; !ok {
		t.Fatalf("expected disbursements key in response, got %v", result)
	}
}
