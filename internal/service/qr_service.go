package service

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

// zambiaLoc is CAT (Central Africa Time) = UTC+2, used for all QR timestamps.
var zambiaLoc = mustLoadLocation("Africa/Lusaka")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		// Fall back to fixed UTC+2 if timezone DB is unavailable
		loc = time.FixedZone("CAT", 2*60*60)
	}
	return loc
}

type QRService struct {
	logger *logrus.Logger
}

func NewQRService(logger *logrus.Logger) *QRService {
	return &QRService{logger: logger}
}

// GenerateQRCode returns a 13-char code: storeId(3) + year(4) + month(2) + day(2) + hour(2).
// Example: "1112026052313" = store 111, 2026-05-23 hour 13 (Zambia time).
func (s *QRService) GenerateQRCode(storeID string) string {
	now := time.Now().In(zambiaLoc)
	return fmt.Sprintf("%s%04d%02d%02d%02d",
		storeID,
		now.Year(),
		int(now.Month()),
		now.Day(),
		now.Hour(),
	)
}

// ValidUntil returns the end-of-hour time for the current QR code.
func (s *QRService) ValidUntil() time.Time {
	now := time.Now().In(zambiaLoc)
	return time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, zambiaLoc)
}

// ValidateQRCode checks that a code is exactly 13 chars, that the embedded storeID
// matches expectedStoreID, and that the embedded timestamp matches the current hour.
func (s *QRService) ValidateQRCode(code, expectedStoreID string) error {
	if len(code) != 13 {
		return errors.New("invalid QR code length")
	}

	parsedStore := code[:3]
	if parsedStore != expectedStoreID {
		return errors.New("QR code does not belong to this store")
	}

	year, err1 := strconv.Atoi(code[3:7])
	month, err2 := strconv.Atoi(code[7:9])
	day, err3 := strconv.Atoi(code[9:11])
	hour, err4 := strconv.Atoi(code[11:13])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return errors.New("invalid QR code format")
	}

	now := time.Now().In(zambiaLoc)
	if year != now.Year() || month != int(now.Month()) || day != now.Day() || hour != now.Hour() {
		return errors.New("QR code has expired")
	}

	return nil
}

// ParseStoreID extracts the 3-char store ID from a QR code without full validation.
func (s *QRService) ParseStoreID(code string) (string, error) {
	if len(code) < 3 {
		return "", errors.New("QR code too short")
	}
	return code[:3], nil
}
