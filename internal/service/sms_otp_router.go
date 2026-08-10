package service

// shouldSendViaAfricaTalking decides the generate/resend provider.
// Twilio when forceTwilio or AT is not configured; otherwise all phones → AT.
func shouldSendViaAfricaTalking(forceTwilio, atConfigured bool) bool {
	return !forceTwilio && atConfigured
}
