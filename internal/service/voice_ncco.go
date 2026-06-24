package service

func ConnectAppNCCO(toUser string) []map[string]any {
	return []map[string]any{{
		"action":   "connect",
		"timeout":  "30",
		"endpoint": []map[string]any{{"type": "app", "user": toUser}},
	}}
}

func RejectNCCO(reason string) []map[string]any {
	return []map[string]any{{
		"action": "talk",
		"text":   rejectionMessage(reason),
	}}
}

func rejectionMessage(reason string) string {
	switch reason {
	case "cap_exceeded":
		return "You have reached the maximum number of calls allowed for this delivery."
	case "trip_not_callable":
		return "Calls are not available for this order at this time."
	case "trip_not_found":
		return "This order could not be found."
	case "unknown_caller":
		return "You are not authorised to make this call."
	default:
		return "The person you are calling is unavailable."
	}
}
