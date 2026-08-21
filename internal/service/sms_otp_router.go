package service

import "strings"

// isIndiaPhone reports whether phone is an E.164 Indian number (+91…).
// Login OTP SMS is skipped for these; users authenticate with masterOTPBypass.
func isIndiaPhone(phone string) bool {
	return strings.HasPrefix(strings.TrimSpace(phone), "+91")
}

// shouldSendViaAfricaTalking decides the generate/resend provider.
// Twilio when forceTwilio or AT is not configured; India (+91) never sends
// via AT (bypass OTP only). Otherwise AT for all phones, including MTN.
func shouldSendViaAfricaTalking(phone string, forceTwilio, atConfigured bool) bool {
	if forceTwilio || !atConfigured {
		return false
	}
	if isIndiaPhone(phone) {
		return false
	}
	return true
}
