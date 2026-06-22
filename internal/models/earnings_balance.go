package models

// IsPositiveCashEarning reports whether a ledger entry contributes to the
// payable cash balance shown to DEs.
func IsPositiveCashEarning(entry *EarningsLedger) bool {
	if entry == nil {
		return false
	}
	if entry.Type == EarningTypeDisbursement {
		return false
	}
	if entry.AmountZMW <= 0 {
		return false
	}
	if isZeroAmountInKind(entry.Type, entry.AmountZMW) {
		return false
	}
	return true
}

func isZeroAmountInKind(earningType EarningType, amountZMW float64) bool {
	if amountZMW != 0 {
		return false
	}
	switch earningType {
	case EarningTypeMealieBag, EarningTypeHouseholdItem, EarningTypeWeeklyGift:
		return true
	default:
		return false
	}
}
