# Prefixed Entity IDs — Counter + Optimus (Design)

**Date:** 2026-06-28
**Status:** Approved (design)
**Scope:** Backend only (qcom / DynamoDB). No inventory/MySQL, frontend, or mobile changes.

## Summary

Today every qcom-owned entity uses a random UUID v4 string as its id
(`uuid.New().String()`), generated inline at ~8 scattered call sites. We will
replace these with **human-readable, type-prefixed, fixed-width ids** of the
form `<2-letter prefix><10 zero-padded digits>` — e.g. a trip becomes
`TR0458047115`, a task `TK…`, a user `US…`.

Ids are produced from a **per-type atomic counter** in DynamoDB, scrambled with
the **same 31-bit Optimus bijection** already used by the Java order-service for
`ORD…` order numbers (so the digits look random and non-sequential but remain
reversible to the counter value). This is applied to **new records only**;
existing UUIDs remain valid and coexist indefinitely.

## Goals

- All new qcom-owned entity ids use the `<PREFIX><10 digits>` format.
- Ids are non-sequential-looking (don't leak volume/ordering) but reversible for
  support/debugging.
- One central `internal/ids` package replaces every inline `uuid.New()` call.
- No new infrastructure beyond items in the existing `QComTable`.

## Non-Goals

- No changes to inventory/MySQL ids (BIGINT PKs, `order_uuid`, `order_number`).
- No backfill/migration of existing records — UUIDs and the new format coexist.
- No frontend / BunzoApp / driver-app / admin-dashboard changes.
- No feature flag — this is a hard switch (rollback = code revert + redeploy).
- No idempotency rework for creation retries (out of scope).
- No externally exposed decode endpoint (internal `Decode()` only).

## Format

```
<2-letter prefix> + <10 zero-padded digits>   (always 12 chars)
e.g.  TR0458047115
```

- Digit payload is a 31-bit Optimus encoding (max id 2^31 − 1 ≈ 2.1B per type),
  left-padded to width 10.
- The 2-letter prefix namespaces each entity type, so the same counter index in
  two types yields the same digits but distinct ids (`TR…` vs `US…`).

## Entity → prefix map

| Entity | ID field | Prefix |
|---|---|---|
| User (customer) | `user_id` | `US` |
| DeliveryExecutive | `de_id` | `DE` |
| Trip | `trip_id` | `TR` |
| Task | `task_id` | `TK` |
| Address | `address_id` | `AD` |
| Dispute | `dispute_id` | `DP` |
| EarningsLedger | `earning_id` | `EA` |
| Disbursement | `disbursement_id` | `DB` |
| CashDepositLedger | `deposit_id` | `CD` |

**Excluded:** `CallRecord` (id comes from Vonage), `referral_code` (already a
6-digit code), `AdminUser` (keyed by username), `Rule` (semantic slug),
`Darkstore` (seeded externally, e.g. `DS-TEST-1`).

## Generation algorithm

For each new entity:

1. **Counter** — atomic `UpdateItem` on `QComTable` item `PK=COUNTER!<TYPE>`,
   `ADD seq 1`, returns the new value `N` (starts at 1; gaps are acceptable).
2. **Optimus encode** — `encoded = ((N * prime) & MAX_ID) ^ xor`, ported
   verbatim from `OrderNumbers.java`, **reusing the same constants**:
   - `prime  = 1580030173`
   - `inverse = 59260789` (for `Decode()`)
   - `xor    = 1163945558`
   - `MAX_ID = 2^31 − 1`
3. **Format** — `<PREFIX> + leftPad(encoded, 10)`.
4. **Store** — assign to the entity's id field and write as today
   (e.g. Trip item under `PK=TRIP!TR0458047115`).

Worked example (trip, `N=1`): `1 * 1580030173 = 1580030173`; `& MAX_ID =
1580030173`; `^ 1163945558 = 458047115`; pad → `0458047115`; final →
`TR0458047115`. `N=2` → `TR2033899500` (non-sequential, as intended).

## Code organization

- New package `internal/ids`:
  - `ids.Next(ctx, ids.Trip) (string, error)` — counter increment + encode +
    prefix.
  - `ids.Decode(id string) (entityType, int64, error)` — reverse, internal only.
  - Entity-type enum carrying its prefix and counter key.
- Replace every `uuid.New().String()` call site for in-scope entities
  (`user_repository.go`, `de_service.go`, `assignment_cron.go` ×3,
  address/dispute/earnings/disbursement/cash-deposit services).

## Coexistence & references

- **New records only.** Existing UUIDs stay valid. Safe because no qcom
  production code validates UUID shape — ids are opaque strings (verified:
  `uuid.Parse`/UUID-regex usage exists only in tests, docs, and curl examples).
- Cross-entity reference fields store the raw id string with no translation:
  - `Trip.order_id` keeps the inventory `ORD…` order number.
  - `Trip.store_id` keeps its existing value.
  - `Trip.customer_user_id` holds whatever that user's id is (old UUID or new
    `US…`).

## Operational notes

- **Failure mode:** counter increment is a new dependency before entity write;
  if it succeeds but the write fails, the number is burned (gap). Acceptable.
- **Hot item:** a single counter item per type caps ~1000 writes/sec; qcom
  volume is far below this. Defer sharding/block-reservation until proven
  necessary.
- **Rollback:** code revert + redeploy (no flag). Already-created `*` ids remain
  valid after revert since both formats coexist.

## Decisions (resolved during design)

1. Scope = qcom-owned entities only.
2. Format = 2-letter prefix + 10 zero-padded digits (fixed 12 chars).
3. Generation = per-type atomic counter + reversible Optimus encode.
4. New records only; UUIDs coexist permanently.
5. Entity/prefix map + exclusions as above.
6. Reuse the exact Java 31-bit Optimus constants; one shared set; per-type
   counters.
7. Counter gaps acceptable; no idempotency work.
8. Hard switch, no feature flag.
9. Counters live in the existing `QComTable` as `COUNTER!<TYPE>` items.
10. Central `internal/ids` package replaces all inline `uuid.New()`.
11. Accept single-hot-counter write ceiling at current scale.
12. Reference fields stored as raw strings; no decode endpoint exposed.
