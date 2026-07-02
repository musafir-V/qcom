package models

import "strings"

// ActivationBlockers returns human-readable reasons this darkstore cannot be
// activated, or nil if ready. Checked generically against every field the
// activation gate requires — name, location, a real serviceable polygon, and
// valid operating hours.
func (d *Darkstore) ActivationBlockers() []string {
	var blockers []string
	if strings.TrimSpace(d.Name) == "" {
		blockers = append(blockers, "name is required")
	}
	// (0,0) treated as "unset" — Null Island is never a real store.
	if d.Latitude == 0 && d.Longitude == 0 {
		blockers = append(blockers, "latitude/longitude must be set")
	}
	if len(d.Polygon) < 3 {
		blockers = append(blockers, "polygon must have at least 3 points")
	}
	if !d.ValidOperatingHours() {
		blockers = append(blockers, "opens_at/closes_at must be valid HH:MM with closes_at after opens_at")
	}
	return blockers
}

// ReadyForActivation reports whether ActivationBlockers is empty.
func (d *Darkstore) ReadyForActivation() bool {
	return len(d.ActivationBlockers()) == 0
}
