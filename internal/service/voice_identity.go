package service

import "strings"

func RiderVonageUser(deID string) string      { return "de_" + deID }
func CustomerVonageUser(userID string) string { return "cust_" + userID }

func ParseVonageUser(u string) (kind, id string, ok bool) {
	switch {
	case strings.HasPrefix(u, "cust_"):
		return "cust", strings.TrimPrefix(u, "cust_"), true
	case strings.HasPrefix(u, "de_"):
		return "de", strings.TrimPrefix(u, "de_"), true
	default:
		return "", "", false
	}
}
