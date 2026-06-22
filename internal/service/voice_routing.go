package service

import "github.com/qcom/qcom/internal/models"

func ResolveCounterpart(trip *models.Trip, callerSub string) (toUser, direction string, ok bool) {
	if trip.DEID != "" && callerSub == RiderVonageUser(trip.DEID) {
		if trip.CustomerUserID == "" {
			return "", "", false
		}
		return CustomerVonageUser(trip.CustomerUserID), "de_to_cust", true
	}
	if trip.CustomerUserID != "" && callerSub == CustomerVonageUser(trip.CustomerUserID) {
		if trip.DEID == "" {
			return "", "", false
		}
		return RiderVonageUser(trip.DEID), "cust_to_de", true
	}
	return "", "", false
}
