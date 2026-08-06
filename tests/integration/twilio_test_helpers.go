//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

var (
	twilioSendMu sync.Mutex
	twilioSends  = map[string]int{}
)

func resetTwilioOTPSends(phone string) {
	twilioSendMu.Lock()
	defer twilioSendMu.Unlock()
	delete(twilioSends, phone)
}

func getTwilioOTPSendCount(phone string) int {
	twilioSendMu.Lock()
	defer twilioSendMu.Unlock()
	return twilioSends[phone]
}

// newSuccessTwilioMockServer accepts Verify start/check calls.
// Start records the To number; Check approves any code.
func newSuccessTwilioMockServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form := string(body)

		switch {
		case strings.Contains(r.URL.Path, "/Verifications") && !strings.Contains(r.URL.Path, "VerificationCheck"):
			to := formValue(form, "To")
			if to != "" {
				twilioSendMu.Lock()
				twilioSends[to]++
				twilioSendMu.Unlock()
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "pending",
				"sid":    "VE_test",
				"to":     to,
				"valid":  false,
			})
		case strings.Contains(r.URL.Path, "/VerificationCheck"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "approved",
				"valid":  true,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":20404}`))
		}
	}))
}

func formValue(form, key string) string {
	for _, part := range strings.Split(form, "&") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == key {
			v := kv[1]
			v = strings.ReplaceAll(v, "%2B", "+")
			return v
		}
	}
	return ""
}
