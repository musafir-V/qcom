package service

import "strings"

// isMTNZambiaTwilioPrefix reports whether phone is an E.164 MTN Zambia number
// in the 76 or 96 national series (+26076… / +26096…), which always use Twilio.
func isMTNZambiaTwilioPrefix(phone string) bool {
	p := strings.TrimSpace(phone)
	return strings.HasPrefix(p, "+26076") || strings.HasPrefix(p, "+26096")
}

// isIndiaPhone reports whether phone is an E.164 Indian number (+91…).
// Login OTP SMS is skipped for these; users authenticate with masterOTPBypass.
func isIndiaPhone(phone string) bool {
	return strings.HasPrefix(strings.TrimSpace(phone), "+91")
}

// shouldSendViaAfricaTalking decides the generate/resend provider.
// Twilio when forceTwilio, AT not configured, or MTN Zambia 76/96 prefix;
// India (+91) never sends via AT (bypass OTP only).
// otherwise AT for all other phones.
func shouldSendViaAfricaTalking(phone string, forceTwilio, atConfigured bool) bool {
	if forceTwilio || !atConfigured {
		return false
	}
	if isMTNZambiaTwilioPrefix(phone) || isIndiaPhone(phone) {
		return false
	}
	return true
}
