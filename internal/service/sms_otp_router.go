package service

// lastNumericDigit returns the last numeric character in phone (e.g. trailing
// formatting is ignored). ok is false when the string has no digits.
func lastNumericDigit(phone string) (digit byte, ok bool) {
	for i := len(phone) - 1; i >= 0; i-- {
		c := phone[i]
		if c >= '0' && c <= '9' {
			return c, true
		}
	}
	return 0, false
}

// phoneEligibleForLocalOTP is true when the phone's last numeric digit is 0 or 1
// (the Africa's Talking ~20% split). Used for verify routing independently of
// whether AT is currently configured.
func phoneEligibleForLocalOTP(phone string) bool {
	d, ok := lastNumericDigit(phone)
	return ok && (d == '0' || d == '1')
}

// shouldSendViaAfricaTalking decides the generate/resend provider.
// Twilio when forceTwilio, AT not configured, or last digit not in {0,1}.
func shouldSendViaAfricaTalking(phone string, forceTwilio, atConfigured bool) bool {
	if forceTwilio || !atConfigured {
		return false
	}
	return phoneEligibleForLocalOTP(phone)
}
