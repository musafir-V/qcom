package service

import "strings"

// isZambiaPhone reports whether phone is an E.164 Zambian number (+260…).
// Login OTP SMS is only sent for these; other countries skip send and
// authenticate with masterOTPBypass.
func isZambiaPhone(phone string) bool {
	return strings.HasPrefix(strings.TrimSpace(phone), "+260")
}

// shouldSendViaAfricaTalking decides the generate/resend provider.
// Twilio when forceTwilio or AT is not configured; non-Zambian numbers never
// send via AT (bypass OTP only). Otherwise AT for Zambian phones.
func shouldSendViaAfricaTalking(phone string, forceTwilio, atConfigured bool) bool {
	if forceTwilio || !atConfigured {
		return false
	}
	return isZambiaPhone(phone)
}
