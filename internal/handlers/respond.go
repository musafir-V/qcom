package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sirupsen/logrus"
)

// writeJSON writes payload as the response body. The status line is already
// committed by the time encoding runs, so a failure cannot be turned into an
// HTTP error — it is logged instead of being discarded. A nil logger falls back
// to the standard logger.
func writeJSON(w http.ResponseWriter, logger *logrus.Logger, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		if logger == nil {
			logger = logrus.StandardLogger()
		}
		logger.WithError(err).WithField("status", status).Error("failed to encode JSON response")
	}
}

// decodeOptionalJSONBody decodes an optional request body. An absent/empty body
// is not an error; malformed JSON is.
func decodeOptionalJSONBody(r *http.Request, dst interface{}) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
