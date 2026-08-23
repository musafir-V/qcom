package handlers

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

// Deposits are written with CashDepositLedger.DEID = rider phone
// (see CashDepositService.RecordDeposit). Listing by the prefixed DE id
// (DE203…) misses every historical row.
func TestCashDepositListKey_UsesPhoneNotPrefixedDEID(t *testing.T) {
	de := &models.DeliveryExecutive{
		DEID:        "DE2033899500",
		PhoneNumber: "+260974889417",
	}
	got := cashDepositListKey(de)
	if got != "+260974889417" {
		t.Fatalf("cashDepositListKey() = %q, want phone %q (not DE id %q)", got, de.PhoneNumber, de.DEID)
	}
}
