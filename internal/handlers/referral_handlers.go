package handlers

import (
	"net/http"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type ReferralHandlers struct {
	referralService *service.ReferralService
	logger          *logrus.Logger
}

func NewReferralHandlers(referralService *service.ReferralService, logger *logrus.Logger) *ReferralHandlers {
	return &ReferralHandlers{referralService: referralService, logger: logger}
}

// GET /api/v1/de/referral
// Returns the DE's referral code and the list of DEs they have referred with status.
func (h *ReferralHandlers) GetReferralScreen(w http.ResponseWriter, r *http.Request) {
	deID, _ := r.Context().Value("entity_id").(string)
	phone, _ := r.Context().Value("phone").(string)

	code, refs, rewardZMW, err := h.referralService.GetReferralScreen(r.Context(), deID, phone)
	if err != nil {
		h.logger.WithError(err).Error("failed to get referral screen")
		h.respondWithError(w, http.StatusInternalServerError, "REFERRAL_FETCH_FAILED", "Failed to fetch referral details")
		return
	}

	type referralItem struct {
		ReferredDEID      string `json:"referred_de_id"`
		ReferredName      string `json:"referred_name,omitempty"`
		Status            string `json:"status"`
		CreatedAt         string `json:"created_at"`
		WindowExpiresAt   string `json:"window_expires_at"`
		PayoutTriggeredAt string `json:"payout_triggered_at,omitempty"`
	}

	items := make([]referralItem, 0, len(refs))
	for _, ref := range refs {
		items = append(items, referralItem{
			ReferredDEID:      ref.ReferredDEID,
			ReferredName:      ref.ReferredName,
			Status:            string(ref.Status),
			CreatedAt:         ref.CreatedAt,
			WindowExpiresAt:   ref.WindowExpiresAt,
			PayoutTriggeredAt: ref.PayoutTriggeredAt,
		})
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"referral_code": code,
		"reward_zmw":    rewardZMW,
		"referrals":     items,
	})
}

func (h *ReferralHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	writeJSON(w, h.logger, status, payload)
}

func (h *ReferralHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
