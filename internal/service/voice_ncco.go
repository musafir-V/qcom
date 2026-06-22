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
		"text":   "The person you are calling is unavailable.",
	}}
}
