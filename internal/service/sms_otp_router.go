package service

import "strings"

// isMTNZambiaTwilioPrefix reports whether phone is an E.164 MTN Zambia number
// in the 76 or 96 national series (+26076… / +26096…), which always use Twilio.
func isMTNZambiaTwilioPrefix(phone string) bool {
	p := strings.TrimSpace(phone)
	return strings.HasPrefix(p, "+26076") || strings.HasPrefix(p, "+26096")
}

// shouldSendViaAfricaTalking decides the generate/resend provider.
// Twilio when forceTwilio, AT not configured, or MTN Zambia 76/96 prefix;
// otherwise AT for all other phones.
func shouldSendViaAfricaTalking(phone string, forceTwilio, atConfigured bool) bool {
	if forceTwilio || !atConfigured {
		return false
	}
	if isMTNZambiaTwilioPrefix(phone) {
		return false
	}
	return true
}
