package models

// DEStatusEventReason enumerates why a DE status transition happened. It powers
// the presence timeline and online-time computation.
type DEStatusEventReason string

const (
	// ReasonScanStart: offline -> eligible via a validated store-QR scan.
	ReasonScanStart DEStatusEventReason = "scan_start"
	// ReasonScanReturn: free -> eligible via a validated store-QR scan.
	ReasonScanReturn DEStatusEventReason = "scan_return"
	// ReasonAssigned: eligible -> busy when a trip is assigned (clock pauses).
	ReasonAssigned DEStatusEventReason = "assigned"
	// ReasonDelivered: busy -> free on trip completion (clock resumes).
	ReasonDelivered DEStatusEventReason = "delivered"
	// ReasonMissedScan: eligible/free -> offline by the sweep at the deadline.
	ReasonMissedScan DEStatusEventReason = "missed_scan"
	// ReasonEndedDuty: eligible/free -> offline via explicit EndDuty.
	ReasonEndedDuty DEStatusEventReason = "ended_duty"
	// ReasonCancelled: busy -> free/eligible when a trip is cancelled.
	ReasonCancelled DEStatusEventReason = "cancelled"
	// ReasonReassigned: busy -> free for the outgoing rider and
	// eligible/free -> busy for the incoming rider on an admin reassignment.
	ReasonReassigned DEStatusEventReason = "reassigned"
)

// DEStatusEvent is one status transition for a DE, stored as an append-only
// log item keyed under the DE's partition:
//
//	PK = DE!{phone}
//	SK = EVENT#{rfc3339_ts}#{id}
//
// The log is the source of truth for the presence timeline / online-time.
type DEStatusEvent struct {
	Phone     string              `json:"phone" dynamodbav:"phone"`
	FromState DEStatus            `json:"from_state" dynamodbav:"from_state"`
	ToState   DEStatus            `json:"to_state" dynamodbav:"to_state"`
	Reason    DEStatusEventReason `json:"reason" dynamodbav:"reason"`
	StoreID   string              `json:"store_id,omitempty" dynamodbav:"store_id,omitempty"`
	Lat       float64             `json:"lat,omitempty" dynamodbav:"lat,omitempty"`
	Lng       float64             `json:"lng,omitempty" dynamodbav:"lng,omitempty"`
	AccuracyM float64             `json:"accuracy_m,omitempty" dynamodbav:"accuracy_m,omitempty"`
	// TS is the RFC3339 UTC timestamp of the transition. It is also embedded in
	// the sort key so day-range queries can be served by the primary index.
	TS string `json:"ts" dynamodbav:"ts"`
}

// StatusEventPK returns the partition key for a DE's status-event log.
func StatusEventPK(phone string) string {
	return "DE!" + phone
}

// StatusEventSK builds the sort key for a status event. id makes the key unique
// when two events share the same timestamp.
func StatusEventSK(ts, id string) string {
	return "EVENT#" + ts + "#" + id
}

// StatusEventSKPrefix returns the sort-key boundary "EVENT#{ts}" (no id suffix),
// used as an inclusive/exclusive bound for day-range BETWEEN queries.
func StatusEventSKPrefix(ts string) string {
	return "EVENT#" + ts
}
