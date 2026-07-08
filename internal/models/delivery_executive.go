package models

import (
	"strings"
	"time"
)

// UnassignedStoreSentinel is the assigned_store_index_key value for a DE that
// has no permanent home darkstore yet. Keeping a sentinel (rather than an empty
// key) keeps every DE present in the AssignedStoreIndex GSI so admins can query
// the "Unassigned" bucket to find and assign them.
const UnassignedStoreSentinel = "UNASSIGNED"

type DEStatus string

const (
	DEStatusOffline  DEStatus = "offline"
	DEStatusEligible DEStatus = "eligible"
	DEStatusBusy     DEStatus = "busy"
	DEStatusFree     DEStatus = "free"
)

// ScanInterval is how long a presence scan (or delivery completion) keeps a DE
// on-duty before the next store-QR scan is due. When the deadline passes the
// sweep flips the DE offline. Applies to eligible and free (busy is paused).
const ScanInterval = 15 * time.Minute

// DutyIndexKeyOnDuty builds the DEDutyIndex partition value for an on-duty DE at
// a store. It is set while the DE is eligible or free and cleared for busy/offline.
func DutyIndexKeyOnDuty(storeID string) string {
	return "DE_ONDUTY#" + storeID
}

// AssignedStoreIndexKeyFor builds the AssignedStoreIndex partition value for a
// DE's permanent home darkstore: the raw store ID when assigned, or the
// UNASSIGNED sentinel when the DE has no assigned store yet. This keeps the GSI
// non-sparse so the "Unassigned" bucket is queryable.
func AssignedStoreIndexKeyFor(assignedStoreID string) string {
	if strings.TrimSpace(assignedStoreID) == "" {
		return UnassignedStoreSentinel
	}
	return assignedStoreID
}

// NameLower normalizes a name for the AssignedStoreIndex sort key, giving
// case-insensitive ordering and begins_with prefix search.
func NameLower(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// IsOnline reports whether a status counts as online for presence accounting:
// eligible, busy, and free are all online; only offline is not.
func (s DEStatus) IsOnline() bool {
	return s == DEStatusEligible || s == DEStatusBusy || s == DEStatusFree
}

type DeliveryExecutive struct {
	DEID        string   `json:"de_id" dynamodbav:"de_id"`
	PhoneNumber string   `json:"phone_number" dynamodbav:"phone_number"`
	Name        string   `json:"name" dynamodbav:"name"`
	ProfileURL  string   `json:"profile_url" dynamodbav:"profile_url"`
	NRCURL           string `json:"nrc_url" dynamodbav:"nrc_url"`
	DriverLicenseURL string `json:"driver_license_url" dynamodbav:"driver_license_url"`
	NRCNumber         string `json:"nrc_number" dynamodbav:"nrc_number"`
	AirtelMoneyNumber string `json:"airtel_money_number" dynamodbav:"airtel_money_number"`
	BikeNumber        string `json:"bike_number" dynamodbav:"bike_number"`
	BikeBrand         string `json:"bike_brand" dynamodbav:"bike_brand"`
	Status      DEStatus `json:"status" dynamodbav:"status"`
	// AssignedStoreID is the DE's permanent "home" darkstore, set by an admin at
	// onboarding (and editable later). Unlike CurrentStoreID (ephemeral live-duty
	// location) this persists across duty sessions. A DE may only start duty by
	// scanning the QR of this store. Empty means unassigned.
	AssignedStoreID string `json:"assigned_store_id,omitempty" dynamodbav:"assigned_store_id,omitempty"`
	// AssignedStoreIndexKey is the AssignedStoreIndex GSI hash key: the assigned
	// store ID, or the UNASSIGNED sentinel when no store is assigned. Always kept
	// in sync with AssignedStoreID so every DE is queryable by store (or the
	// Unassigned bucket).
	AssignedStoreIndexKey string `json:"assigned_store_index_key,omitempty" dynamodbav:"assigned_store_index_key,omitempty"`
	// NameLower is the lowercased Name, used as the AssignedStoreIndex sort key
	// for case-insensitive name ordering and begins_with prefix search.
	NameLower string `json:"name_lower,omitempty" dynamodbav:"name_lower,omitempty"`
	// Set to "DE_ONDUTY#{storeId}" while the DE is eligible OR free (on-duty),
	// cleared for busy/offline. Used by the DEDutyIndex GSI so both the
	// assignment cron (filter status=eligible) and the presence sweep
	// (eligible+free) can query on-duty DEs by store.
	DutyIndexKey        string `json:"duty_index_key,omitempty" dynamodbav:"duty_index_key,omitempty"`
	// Presence-tracking fields. ScanDeadlineAt is the RFC3339 UTC time by which
	// the DE must re-scan the store QR or be flipped offline by the sweep; it is
	// set on scan (eligible) and delivery completion (free), and cleared when
	// busy/offline. LastScan* stamp the most recent validated presence scan.
	ScanDeadlineAt string  `json:"scan_deadline_at,omitempty" dynamodbav:"scan_deadline_at,omitempty"`
	LastScanLat    float64 `json:"last_scan_lat,omitempty" dynamodbav:"last_scan_lat,omitempty"`
	LastScanLng    float64 `json:"last_scan_lng,omitempty" dynamodbav:"last_scan_lng,omitempty"`
	LastScanAt     string  `json:"last_scan_at,omitempty" dynamodbav:"last_scan_at,omitempty"`
	CurrentStoreID      string `json:"current_store_id,omitempty" dynamodbav:"current_store_id,omitempty"`
	CurrentOrderID      string `json:"current_order_id,omitempty" dynamodbav:"current_order_id,omitempty"`
	CurrentTripID       string `json:"current_trip_id,omitempty" dynamodbav:"current_trip_id,omitempty"`
	TotalTripsCompleted int    `json:"total_trips_completed" dynamodbav:"total_trips_completed"`
	DailyTripCount      int    `json:"daily_trip_count" dynamodbav:"daily_trip_count"`
	DailyCountDate      string `json:"daily_count_date,omitempty" dynamodbav:"daily_count_date,omitempty"`
	InHandCashZMW       float64 `json:"in_hand_cash_zmw" dynamodbav:"in_hand_cash_zmw"`
	LastDisbursedAt     string `json:"last_disbursed_at,omitempty" dynamodbav:"last_disbursed_at,omitempty"`
	ReferralCode        string `json:"referral_code,omitempty" dynamodbav:"referral_code,omitempty"`
	CreatedAt           string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt           string `json:"updated_at" dynamodbav:"updated_at"`
}

func (de *DeliveryExecutive) GetPK() string {
	return "DE!" + de.PhoneNumber
}

func (de *DeliveryExecutive) GetSK() string {
	return "METADATA"
}

// TripsToday returns the DE's completed trip count for `today` (Zambia date,
// "2006-01-02"). DailyTripCount only resets when a trip completes on a new day,
// so a stale DailyCountDate means zero trips have happened today.
func (de *DeliveryExecutive) TripsToday(today string) int {
	if de.DailyCountDate != today {
		return 0
	}
	return de.DailyTripCount
}

// CashExceeds reports whether the DE's in-hand COD cash is strictly above the
// given limit. At exactly the limit the DE is still eligible.
func (de *DeliveryExecutive) CashExceeds(limit float64) bool {
	return de.InHandCashZMW > limit
}
