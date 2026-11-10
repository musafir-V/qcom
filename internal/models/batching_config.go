package models

import (
	"strconv"
	"strings"
)

// BatchingConfig is the DynamoDB singleton PK=CONFIG SK=BATCHING_V1.
// Missing row → Enabled=false (merge APIs return 403 BATCHING_DISABLED).
type BatchingConfig struct {
	Enabled             bool   `json:"enabled" dynamodbav:"enabled"`
	MinDriverAppVersion string `json:"min_driver_app_version" dynamodbav:"min_driver_app_version"`
}

func (c *BatchingConfig) GetPK() string { return "CONFIG" }
func (c *BatchingConfig) GetSK() string { return "BATCHING_V1" }

// AppVersionOK reports whether lastAppVersion meets MinDriverAppVersion.
// Empty lastAppVersion is always old. Empty min means any non-empty version is OK.
func (c *BatchingConfig) AppVersionOK(lastAppVersion string) bool {
	last := strings.TrimSpace(lastAppVersion)
	if last == "" {
		return false
	}
	min := ""
	if c != nil {
		min = strings.TrimSpace(c.MinDriverAppVersion)
	}
	if min == "" {
		return true
	}
	return compareSemver(last, min) >= 0
}

// compareSemver compares dotted numeric semver strings (major.minor.patch).
// Extra/missing segments are treated as 0. Non-numeric segments compare as 0.
func compareSemver(a, b string) int {
	as := semverParts(a)
	bs := semverParts(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func semverParts(s string) []int {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return nil
	}
	raw := strings.Split(s, ".")
	out := make([]int, 0, len(raw))
	for _, p := range raw {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			n = 0
		}
		out = append(out, n)
	}
	return out
}
