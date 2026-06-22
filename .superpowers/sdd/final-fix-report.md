# Final Fix Report — Driver Earnings & Payout Rules Engine

## C1 — Outstanding balance now counts all positive cash earnings

- Updated `internal/handlers/earnings_handlers.go` `computeBreakdown` to use a shared predicate (`models.IsPositiveCashEarning`) instead of allow-listing only `trip` + `b1_daily_bonus`.
- New rule now:
  - excludes `disbursement` mirror entries,
  - excludes non-positive amounts,
  - excludes zero-amount in-kind reward rows (`mealie_bag`, `household_item`, `weekly_gift`),
  - includes all other positive cash rows by default.
- Category split is now future-proof:
  - `trip` -> `live_order_total_zmw`
  - all other included cash -> `bonus_total_zmw` (covers `b1_daily_bonus`, `referral_bonus`, `weekly_bonus`, future cash types).

### C1 coverage added

- Updated `internal/handlers/earnings_handlers_test.go` to include:
  - `trip` + `b1_daily_bonus` + `referral_bonus` (cash)
  - zero-amount in-kind row
  - negative disbursement mirror row
- Assertions now verify:
  - `outstanding_balance_zmw` equals only the three positive cash rows,
  - `bonus_total_zmw` includes referral cash.

## C1b — Home-screen balance now uses identical cash-only definition

- Added `SumPositiveCashByDEAfter` in `internal/repository/earnings_ledger_repository.go`.
- `internal/service/de_service.go` `GetTodayEarnings` now calls `SumPositiveCashByDEAfter` (not all-types `SumByDEAfter`), so home-screen balance excludes disbursement mirror and still includes referral/other positive cash.
- Added shared logic file `internal/models/earnings_balance.go` and reused it in both:
  - earnings summary breakdown path,
  - DE home-screen balance summation path.

### C1b coverage added

- Added `internal/service/de_service_test.go`:
  - validates today earnings include trip + b1 + referral,
  - validates exclusion of zero in-kind and negative disbursement,
  - validates timestamp boundary call uses `timezone.StartOfDayString()`.

### Call-site safety check (C1b)

- Verified `SumByDEAfter` callers via search:
  - only DE service previously depended on it for home-screen balance.
- Kept `SumByDEAfter` intact for any future/all-types consumers.
- Added dedicated cash-only path instead of changing all-types behavior.

## C2 — Legacy weekly bonus cron removed

- Removed legacy weekly bonus cron wiring from `cmd/server/main.go`:
  - removed construction,
  - removed `.Start()`,
  - removed shutdown `.Stop()`,
  - removed now-unused `weeklySummaryRepo` construction.
- Removed unused legacy implementation files:
  - `internal/service/weekly_bonus_cron.go`
  - `internal/service/weekly_bonus_cron_test.go`
- Removed now-unused payout service API:
  - `PayoutService.WriteWeeklyBonusEntry` from `internal/service/payout_service.go`.

### Call-site safety check (C2)

- Verified no runtime callers remain for `WriteWeeklyBonusEntry` and `WeeklyBonusCron` in code (only docs mention historical design).
- Referral bonus logic remains unchanged and intact.

## I2 — Completion payout now rounds once at final total

- Updated `internal/service/payout_service.go` `computeCompletionPayout`:
  - compute unrounded `rawBasePayZMW := computeBasePay(...)`,
  - compute final pay once via `ApplyRate(rawBasePayZMW, decision)` (single Round2),
  - store `BasePayZMW = Round2(rawBasePayZMW)` for display,
  - compute `BonusPayZMW = Round2(total - BasePayZMW)`.
- This removes double-rounding behavior at completion while preserving frozen-rate decisioning.

### I2 coverage added

- Added `TestComputeCompletionPayout_RoundsOnlyFinalTotal` in `internal/service/payout_service_test.go` with a rounding-sensitive base scenario to assert single-round final pay behavior.

## Verification commands and results

- `go test ./internal/handlers/... ./internal/service/... -v` -> **PASS**
- `go test ./...` -> **PASS**
- `go build ./...` -> **PASS**

All requested fixes (C1, C1b, C2, I2) are implemented and verified without editing `bin/` artifacts.
