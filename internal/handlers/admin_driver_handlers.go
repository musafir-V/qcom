package handlers

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

// AdminDriverHandlers powers the admin dashboard's driver-centric flows:
// lookup-by-phone detail aggregation, paginated sub-resource reads (earnings,
// disbursements, referrals, cash ledger), onboarding photo presign, and
// admin-driven driver creation. All routes sit behind AdminKeyMiddleware.
type AdminDriverHandlers struct {
	deService        *service.DEService
	deRepo           *repository.DERepository
	tripService      *service.TripService
	payoutConfigRepo *repository.PayoutConfigRepository
	cashConfigRepo   *repository.CashConfigRepository
	cashLedgerRepo   *repository.CashDepositLedgerRepository
	uploadService    *service.UploadService
	presenceService  *service.PresenceService
	earningsHandlers *EarningsHandlers
	referralHandlers *ReferralHandlers
	inKindHandlers   *InKindDisbursementHandlers
	bucket           string
	logger           *logrus.Logger
}

func NewAdminDriverHandlers(
	deService *service.DEService,
	deRepo *repository.DERepository,
	tripService *service.TripService,
	payoutConfigRepo *repository.PayoutConfigRepository,
	cashConfigRepo *repository.CashConfigRepository,
	cashLedgerRepo *repository.CashDepositLedgerRepository,
	uploadService *service.UploadService,
	presenceService *service.PresenceService,
	earningsHandlers *EarningsHandlers,
	referralHandlers *ReferralHandlers,
	inKindHandlers *InKindDisbursementHandlers,
	bucket string,
	logger *logrus.Logger,
) *AdminDriverHandlers {
	return &AdminDriverHandlers{
		deService:        deService,
		deRepo:           deRepo,
		tripService:      tripService,
		payoutConfigRepo: payoutConfigRepo,
		cashConfigRepo:   cashConfigRepo,
		cashLedgerRepo:   cashLedgerRepo,
		uploadService:    uploadService,
		presenceService:  presenceService,
		earningsHandlers: earningsHandlers,
		referralHandlers: referralHandlers,
		inKindHandlers:   inKindHandlers,
		bucket:           bucket,
		logger:           logger,
	}
}

// normalizePhone trims the path param and ensures a leading "+".
func normalizePhone(raw string) string {
	p := strings.TrimSpace(raw)
	if p != "" && !strings.HasPrefix(p, "+") {
		p = "+" + p
	}
	return p
}

// GET /api/v1/admin/drivers/{phone}
// Aggregated driver detail: profile, docs (view URLs), live status, cash, trips,
// today's earnings, and daily milestone progress.
func (h *AdminDriverHandlers) GetDriver(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	if phone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "phone is required")
		return
	}

	de, err := h.deRepo.GetByPhone(r.Context(), phone)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to fetch driver")
		respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch driver")
		return
	}
	if de == nil {
		respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "Driver not found")
		return
	}

	todayEarnings, err := h.deService.GetTodayEarnings(r.Context(), de.DEID)
	if err != nil {
		h.logger.WithError(err).Warn("admin: failed to compute today's earnings; defaulting to 0")
		todayEarnings = 0
	}

	tripsToday := de.TripsToday(timezone.DateString())

	resp := map[string]interface{}{
		"de_id":                 de.DEID,
		"phone_number":          de.PhoneNumber,
		"name":                  de.Name,
		"status":                de.Status,
		"profile_url":           de.ProfileURL,
		"profile_view_url":      h.docViewURL(r.Context(), de.ProfileURL),
		"nrc_url":               de.NRCURL,
		"nrc_view_url":          h.docViewURL(r.Context(), de.NRCURL),
		"driver_license_url":    de.DriverLicenseURL,
		"driver_license_view_url": h.docViewURL(r.Context(), de.DriverLicenseURL),
		"nrc_number":            de.NRCNumber,
		"airtel_money_number":   de.AirtelMoneyNumber,
		"bike_number":           de.BikeNumber,
		"bike_brand":            de.BikeBrand,
		"referral_code":         de.ReferralCode,
		"assigned_store_id":     de.AssignedStoreID,
		"current_store_id":      de.CurrentStoreID,
		"current_order_id":      de.CurrentOrderID,
		"current_trip_id":       de.CurrentTripID,
		"trips_today":           tripsToday,
		"total_trips_completed": de.TotalTripsCompleted,
		"in_hand_cash_zmw":      de.InHandCashZMW,
		"today_earnings_zmw":    todayEarnings,
		"last_disbursed_at":     de.LastDisbursedAt,
		"created_at":            de.CreatedAt,
		"updated_at":            de.UpdatedAt,
	}

	if payoutCfg, err := h.payoutConfigRepo.Get(r.Context()); err != nil {
		h.logger.WithError(err).Warn("admin: failed to fetch payout config; omitting daily_milestone")
	} else {
		resp["daily_milestone"] = service.ComputeDailyMilestone(tripsToday, payoutCfg)
	}

	cashCfg, err := h.cashConfigRepo.Get(r.Context())
	if err != nil {
		h.logger.WithError(err).Warn("admin: failed to fetch cash config; defaulting limit")
		cashCfg = &models.CashConfig{}
	}
	resp["cash_limit_zmw"] = cashCfg.EffectiveLimitZMW()
	resp["cash_blocked"] = de.CashExceeds(cashCfg.EffectiveLimitZMW())

	respondWithJSON(w, http.StatusOK, resp)
}

// docViewURL returns a displayable URL for a stored document value. Legacy
// records store full http(s) URLs (returned as-is); admin-onboarded records
// store S3 object keys, which we presign for short-lived GET access.
func (h *AdminDriverHandlers) docViewURL(ctx context.Context, stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	if strings.HasPrefix(stored, "http://") || strings.HasPrefix(stored, "https://") {
		return stored
	}
	url, err := h.uploadService.PresignGetURL(ctx, h.bucket, stored)
	if err != nil {
		h.logger.WithError(err).Warn("admin: failed to presign doc view URL")
		return ""
	}
	return url
}

// GET /api/v1/admin/drivers/{phone}/trip
// Returns the driver's current active trip with task/item details (OTP redacted).
func (h *AdminDriverHandlers) GetDriverTrip(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	if phone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "phone is required")
		return
	}

	trip, err := h.tripService.GetCurrentTrip(r.Context(), phone)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to fetch driver trip")
		respondWithError(w, http.StatusInternalServerError, "TRIP_FETCH_FAILED", "Failed to fetch trip")
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"trip": redactTripForAdmin(trip),
	})
}

// POST /api/v1/admin/drivers/{phone}/trip/pickup/complete
// Marks pickup done without bill QR verification.
func (h *AdminDriverHandlers) AdminCompletePickup(w http.ResponseWriter, r *http.Request) {
	h.adminCompleteTask(w, r, models.TaskTypePickup, "")
}

// POST /api/v1/admin/drivers/{phone}/trip/drop/complete
// Body: { "otp": "1234" }
func (h *AdminDriverHandlers) AdminCompleteDrop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OTP string `json:"otp"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	h.adminCompleteTask(w, r, models.TaskTypeDrop, req.OTP)
}

func (h *AdminDriverHandlers) adminCompleteTask(w http.ResponseWriter, r *http.Request, taskType models.TaskType, otp string) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	if phone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "phone is required")
		return
	}

	err := h.tripService.AdminCompleteTask(r.Context(), phone, taskType, otp)
	if err != nil {
		status, code := classifyTaskUpdateError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("admin: failed to complete trip task")
			respondWithError(w, status, code, "Failed to complete trip task")
			return
		}
		respondWithError(w, status, code, err.Error())
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// adminTripView is the trip as ops sees it: OTP stripped, reassignment history
// added. The history is deliberately absent from models.Trip's JSON (it names
// the ops admin and carries reason codes about riders), so it is surfaced here.
type adminTripView struct {
	*models.Trip
	Reassignments []models.TripReassignment `json:"reassignments,omitempty"`
}

func redactTripForAdmin(trip *models.Trip) *adminTripView {
	if trip == nil {
		return nil
	}
	clone := *trip
	tasks := make([]models.Task, len(trip.Tasks))
	for i, task := range trip.Tasks {
		tasks[i] = task
		tasks[i].OTP = ""
	}
	clone.Tasks = tasks
	return &adminTripView{Trip: &clone, Reassignments: trip.Reassignments}
}

// GET /api/v1/admin/drivers/{phone}/earnings
// Delegates to the DE earnings handler with phone injected from the path.
func (h *AdminDriverHandlers) GetDriverEarnings(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	ctx := context.WithValue(r.Context(), "phone", phone)
	h.earningsHandlers.GetEarningsSummary(w, r.WithContext(ctx))
}

// GET /api/v1/admin/drivers/{phone}/disbursements
func (h *AdminDriverHandlers) GetDriverDisbursements(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	ctx := context.WithValue(r.Context(), "phone", phone)
	h.earningsHandlers.GetDisbursements(w, r.WithContext(ctx))
}

// GET /api/v1/admin/drivers/{phone}/referrals
// The referral handler keys on entity_id (de_id) + phone, so resolve the driver first.
func (h *AdminDriverHandlers) GetDriverReferrals(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	de, err := h.deRepo.GetByPhone(r.Context(), phone)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to fetch driver for referrals")
		respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch driver")
		return
	}
	if de == nil {
		respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "Driver not found")
		return
	}
	ctx := context.WithValue(r.Context(), "phone", phone)
	ctx = context.WithValue(ctx, "entity_id", de.DEID)
	ctx = context.WithValue(ctx, "entity_type", "de")
	h.referralHandlers.GetReferralScreen(w, r.WithContext(ctx))
}

// GET /api/v1/admin/drivers/{phone}/cash-ledger
func (h *AdminDriverHandlers) GetDriverCashLedger(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	de, err := h.deRepo.GetByPhone(r.Context(), phone)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to fetch driver for cash ledger")
		respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch driver")
		return
	}
	if de == nil {
		respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "Driver not found")
		return
	}

	entries, err := h.cashLedgerRepo.ListByDE(r.Context(), de.DEID)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to list cash ledger")
		respondWithError(w, http.StatusInternalServerError, "CASH_LEDGER_FETCH_FAILED", "Failed to fetch cash deposits")
		return
	}

	type item struct {
		DepositID          string  `json:"deposit_id"`
		RequestedAmountZMW float64 `json:"requested_amount_zmw"`
		AppliedAmountZMW   float64 `json:"applied_amount_zmw"`
		CreatedAt          string  `json:"created_at"`
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		items = append(items, item{
			DepositID:          e.DepositID,
			RequestedAmountZMW: e.RequestedAmountZMW,
			AppliedAmountZMW:   e.AppliedAmountZMW,
			CreatedAt:          e.CreatedAt,
		})
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"in_hand_cash_zmw": de.InHandCashZMW,
		"deposits":         items,
	})
}

// GET /api/v1/admin/drivers/{phone}/presence?date=YYYY-MM-DD
// Returns the driver's online segments and total online minutes for the given
// Zambia day (defaults to today), computed from the status-event log.
func (h *AdminDriverHandlers) GetDriverPresence(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	if phone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "phone is required")
		return
	}

	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			respondWithError(w, http.StatusBadRequest, "INVALID_DATE", "date must be YYYY-MM-DD")
			return
		}
	}

	report, err := h.presenceService.GetDayPresence(r.Context(), phone, date)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to compute driver presence")
		respondWithError(w, http.StatusInternalServerError, "PRESENCE_FETCH_FAILED", "Failed to compute presence")
		return
	}

	respondWithJSON(w, http.StatusOK, report)
}

// uploadDocExt maps allowed MIME types to a canonical extension for onboarding docs.
var uploadDocExt = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/heic":      ".heic",
	"application/pdf": ".pdf",
}

// POST /api/v1/admin/uploads/url
// Presigns a direct-to-S3 PUT for a driver onboarding document. The driver record
// need not exist yet; objects are keyed under drivers/{phone}/{kind}/.
func (h *AdminDriverHandlers) PresignDriverDoc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind     string `json:"kind"`
		Phone    string `json:"phone"`
		FileName string `json:"file_name"`
		FileType string `json:"file_type"`
		FileSize int64  `json:"file_size"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	switch kind {
	case "profile", "nrc", "license":
	default:
		respondWithError(w, http.StatusBadRequest, "INVALID_KIND", "kind must be one of: profile, nrc, license")
		return
	}

	phone := normalizePhone(req.Phone)
	if phone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "phone is required")
		return
	}
	if strings.TrimSpace(req.FileType) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "file_type is required")
		return
	}

	ext := uploadDocExt[req.FileType]
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(req.FileName))
	}

	// Strip the leading "+" so the S3 key stays URL-safe.
	phoneKey := strings.TrimPrefix(phone, "+")
	objectKey := fmt.Sprintf("drivers/%s/%s/%s%s", phoneKey, kind, uuid.New().String(), ext)

	result, err := h.uploadService.GeneratePresignedPutURL(r.Context(), h.bucket, objectKey, req.FileType, req.FileSize)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to presign driver doc")
		respondWithError(w, http.StatusInternalServerError, "PRESIGN_FAILED", "Failed to generate upload URL")
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"upload_url":         result.UploadURL,
		"object_key":         result.ObjectKey,
		"expires_in_seconds": result.ExpiresInSeconds,
	})
}

// POST /api/v1/admin/drivers
// Admin-driven driver onboarding. Wraps DEService.Register with the same validation
// as self-service registration; *_url fields are typically S3 object keys produced
// by PresignDriverDoc.
func (h *AdminDriverHandlers) CreateDriver(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PhoneNumber       string `json:"phone_number"`
		Name              string `json:"name"`
		ProfileURL        string `json:"profile_url"`
		NRCURL            string `json:"nrc_url"`
		DriverLicenseURL  string `json:"driver_license_url"`
		NRCNumber         string `json:"nrc_number"`
		AirtelMoneyNumber string `json:"airtel_money_number"`
		BikeNumber        string `json:"bike_number"`
		BikeBrand         string `json:"bike_brand"`
		AssignedStoreID   string `json:"assigned_store_id"`
		ReferralCode      string `json:"referral_code"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}

	req.PhoneNumber = normalizePhone(req.PhoneNumber)
	if !isValidPhoneNumber(req.PhoneNumber) {
		respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "name is required")
		return
	}
	if strings.TrimSpace(req.ProfileURL) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "profile_url is required")
		return
	}
	if strings.TrimSpace(req.NRCURL) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "nrc_url is required")
		return
	}
	if strings.TrimSpace(req.DriverLicenseURL) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "driver_license_url is required")
		return
	}
	if strings.TrimSpace(req.NRCNumber) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "nrc_number is required")
		return
	}
	if strings.TrimSpace(req.AirtelMoneyNumber) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "airtel_money_number is required")
		return
	}
	if strings.TrimSpace(req.BikeNumber) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "bike_number is required")
		return
	}
	if strings.TrimSpace(req.BikeBrand) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "bike_brand is required")
		return
	}
	if strings.TrimSpace(req.AssignedStoreID) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "assigned_store_id is required")
		return
	}

	de, err := h.deService.Register(r.Context(), service.RegisterDERequest{
		PhoneNumber:       req.PhoneNumber,
		Name:              req.Name,
		ProfileURL:        req.ProfileURL,
		NRCURL:            req.NRCURL,
		DriverLicenseURL:  req.DriverLicenseURL,
		NRCNumber:         strings.TrimSpace(req.NRCNumber),
		AirtelMoneyNumber: strings.TrimSpace(req.AirtelMoneyNumber),
		BikeNumber:        strings.TrimSpace(req.BikeNumber),
		BikeBrand:         strings.TrimSpace(req.BikeBrand),
		AssignedStoreID:   strings.TrimSpace(req.AssignedStoreID),
		ReferralCode:      req.ReferralCode,
	})
	if err != nil {
		if strings.Contains(err.Error(), "already registered") {
			respondWithError(w, http.StatusConflict, "DE_ALREADY_EXISTS", err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			respondWithError(w, http.StatusBadRequest, "STORE_NOT_FOUND", err.Error())
			return
		}
		h.logger.WithError(err).Error("admin: failed to register driver")
		respondWithError(w, http.StatusInternalServerError, "REGISTRATION_FAILED", "Failed to register driver")
		return
	}

	respondWithJSON(w, http.StatusCreated, map[string]interface{}{
		"de_id":         de.DEID,
		"phone_number":  de.PhoneNumber,
		"name":          de.Name,
		"status":        de.Status,
		"referral_code": de.ReferralCode,
		"created_at":    de.CreatedAt,
	})
}

// defaultDriverListLimit / maxDriverListLimit bound the list endpoint's page size.
const (
	defaultDriverListLimit = 50
	maxDriverListLimit     = 100
)

// GET /api/v1/admin/drivers?assigned_store_id=&name=&cursor=&limit=
// Lists drivers assigned to a darkstore (or the Unassigned bucket), ordered by
// name, with optional case-insensitive name prefix filter and cursor pagination.
// Pass assigned_store_id="UNASSIGNED" (or empty) for unassigned drivers.
func (h *AdminDriverHandlers) ListDrivers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, ok := q["assigned_store_id"]; !ok {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "assigned_store_id is required (use UNASSIGNED for unassigned drivers)")
		return
	}
	storeID := strings.TrimSpace(q.Get("assigned_store_id"))
	if strings.EqualFold(storeID, models.UnassignedStoreSentinel) {
		storeID = ""
	}
	namePrefix := strings.TrimSpace(q.Get("name"))
	cursor := strings.TrimSpace(q.Get("cursor"))

	limit := int32(defaultDriverListLimit)
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > maxDriverListLimit {
				n = maxDriverListLimit
			}
			limit = int32(n)
		}
	}

	des, nextCursor, err := h.deService.ListDriversByStore(r.Context(), storeID, namePrefix, cursor, limit)
	if err != nil {
		if strings.Contains(err.Error(), "invalid cursor") {
			respondWithError(w, http.StatusBadRequest, "INVALID_CURSOR", "Invalid pagination cursor")
			return
		}
		h.logger.WithError(err).Error("admin: failed to list drivers")
		respondWithError(w, http.StatusInternalServerError, "DRIVER_LIST_FAILED", "Failed to list drivers")
		return
	}

	type driverSummary struct {
		DEID            string `json:"de_id"`
		PhoneNumber     string `json:"phone_number"`
		Name            string `json:"name"`
		Status          string `json:"status"`
		ProfileURL      string `json:"profile_url"`
		ProfileViewURL  string `json:"profile_view_url"`
		AssignedStoreID string `json:"assigned_store_id"`
	}
	items := make([]driverSummary, 0, len(des))
	for _, de := range des {
		items = append(items, driverSummary{
			DEID:            de.DEID,
			PhoneNumber:     de.PhoneNumber,
			Name:            de.Name,
			Status:          string(de.Status),
			ProfileURL:      de.ProfileURL,
			ProfileViewURL:  h.docViewURL(r.Context(), de.ProfileURL),
			AssignedStoreID: de.AssignedStoreID,
		})
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"drivers":     items,
		"next_cursor": nextCursor,
	})
}

// PATCH /api/v1/admin/drivers/{phone}/assigned-store
// Body: { "assigned_store_id": "221" }  (empty string unassigns).
// Reassigns a driver's permanent home darkstore. The store must exist (inactive
// allowed). Active duty is not disturbed.
func (h *AdminDriverHandlers) UpdateAssignedStore(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	if phone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "phone is required")
		return
	}

	var req struct {
		AssignedStoreID string `json:"assigned_store_id"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	storeID := strings.TrimSpace(req.AssignedStoreID)

	if err := h.deService.ReassignStore(r.Context(), phone, storeID); err != nil {
		errStr := err.Error()
		switch {
		case strings.Contains(errStr, "delivery executive not found"):
			respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "Driver not found")
		case strings.Contains(errStr, "not found"):
			respondWithError(w, http.StatusBadRequest, "STORE_NOT_FOUND", errStr)
		default:
			h.logger.WithError(err).Error("admin: failed to reassign driver store")
			respondWithError(w, http.StatusInternalServerError, "REASSIGN_FAILED", "Failed to update assigned store")
		}
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"phone_number":      phone,
		"assigned_store_id": storeID,
	})
}

// RecordInKindDisbursement handles POST /api/v1/admin/drivers/{phone}/inkind-disbursements
func (h *AdminDriverHandlers) RecordInKindDisbursement(w http.ResponseWriter, r *http.Request) {
	h.inKindHandlers.RecordInKindDisbursement(w, r)
}

// ListInKindDisbursements handles GET /api/v1/admin/drivers/{phone}/inkind-disbursements
func (h *AdminDriverHandlers) ListInKindDisbursements(w http.ResponseWriter, r *http.Request) {
	h.inKindHandlers.ListInKindDisbursements(w, r)
}
