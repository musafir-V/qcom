package service

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

func newTestQRService() *QRService {
	return NewQRService(logrus.New())
}

// A1 — output is always exactly 13 chars and starts with the store ID
func TestGenerateQRCode_Format(t *testing.T) {
	svc := newTestQRService()

	for _, storeID := range []string{"111", "112", "999"} {
		code := svc.GenerateQRCode(storeID)
		if len(code) != 13 {
			t.Errorf("storeID=%s: expected length 13, got %d (%q)", storeID, len(code), code)
		}
		if code[:3] != storeID {
			t.Errorf("storeID=%s: code %q does not start with store ID", storeID, code)
		}
	}
}

// A2 — embedded hour matches the current Zambia hour
func TestGenerateQRCode_CurrentHour(t *testing.T) {
	svc := newTestQRService()
	now := time.Now().In(timezone.ZambiaLocation())

	code := svc.GenerateQRCode("111")

	year, _ := strconv.Atoi(code[3:7])
	month, _ := strconv.Atoi(code[7:9])
	day, _ := strconv.Atoi(code[9:11])
	hour, _ := strconv.Atoi(code[11:13])

	if year != now.Year() {
		t.Errorf("expected year %d, got %d", now.Year(), year)
	}
	if month != int(now.Month()) {
		t.Errorf("expected month %d, got %d", int(now.Month()), month)
	}
	if day != now.Day() {
		t.Errorf("expected day %d, got %d", now.Day(), day)
	}
	if hour != now.Hour() {
		t.Errorf("expected hour %d, got %d", now.Hour(), hour)
	}
}

// A3 — a freshly generated code validates successfully
func TestValidateQRCode_CurrentHour_Pass(t *testing.T) {
	svc := newTestQRService()
	code := svc.GenerateQRCode("111")

	if err := svc.ValidateQRCode(code, "111"); err != nil {
		t.Errorf("expected no error for current-hour QR, got: %v", err)
	}
}

// A4 — a code stamped with a past year is always expired
func TestValidateQRCode_ExpiredCode_Fail(t *testing.T) {
	svc := newTestQRService()
	// Hard-coded past timestamp: store 111, year 2020, Jan 1, hour 00
	expiredCode := "1112020010100"

	err := svc.ValidateQRCode(expiredCode, "111")
	if err == nil {
		t.Error("expected error for expired QR, got nil")
	}
}

// A5 — a code whose embedded store ID doesn't match expectedStoreID is rejected
func TestValidateQRCode_WrongStore_Fail(t *testing.T) {
	svc := newTestQRService()
	// Generate a valid code for store 112
	codeFor112 := svc.GenerateQRCode("112")

	// Validate it claiming it belongs to store 111 — should fail
	err := svc.ValidateQRCode(codeFor112, "111")
	if err == nil {
		t.Error("expected error when store ID in code doesn't match expectedStoreID")
	}
}

// A6 — codes with wrong length are rejected
func TestValidateQRCode_BadLength_Fail(t *testing.T) {
	svc := newTestQRService()

	cases := []string{
		"",           // empty
		"111",        // only store ID
		"11120260523", // 11 chars
		"111202605231300", // 15 chars
	}

	for _, code := range cases {
		if err := svc.ValidateQRCode(code, "111"); err == nil {
			t.Errorf("expected error for code %q (len %d), got nil", code, len(code))
		}
	}
}

// ValidUntil should return a time in the future (top of current or next hour)
func TestValidUntil_IsFuture(t *testing.T) {
	svc := newTestQRService()
	until := svc.ValidUntil()

	if !until.After(time.Now()) {
		t.Errorf("ValidUntil %v should be in the future", until)
	}
}

// ParseStoreID extracts the first 3 chars
func TestParseStoreID(t *testing.T) {
	svc := newTestQRService()

	cases := []struct {
		code    string
		want    string
		wantErr bool
	}{
		{"1112026052313", "111", false},
		{"9992026052313", "999", false},
		{"ab", "", true}, // too short
	}

	for _, tc := range cases {
		got, err := svc.ParseStoreID(tc.code)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseStoreID(%q): expected error", tc.code)
			}
		} else {
			if err != nil {
				t.Errorf("ParseStoreID(%q): unexpected error: %v", tc.code, err)
			}
			if got != tc.want {
				t.Errorf("ParseStoreID(%q): got %q, want %q", tc.code, got, tc.want)
			}
		}
	}
}

// Full round-trip: generate → validate with correct and wrong store
func TestQRCode_RoundTrip(t *testing.T) {
	svc := newTestQRService()

	for _, storeID := range []string{"111", "222", "999"} {
		code := svc.GenerateQRCode(storeID)

		// Should pass with correct store
		if err := svc.ValidateQRCode(code, storeID); err != nil {
			t.Errorf("storeID=%s: valid code failed: %v", storeID, err)
		}

		// Should fail with different store
		otherStore := fmt.Sprintf("%03d", (func() int {
			n, _ := strconv.Atoi(storeID)
			return n + 1
		})())
		if err := svc.ValidateQRCode(code, otherStore); err == nil {
			t.Errorf("storeID=%s: cross-store validation should have failed", storeID)
		}
	}
}
