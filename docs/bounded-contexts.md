# Bunzo qcom — Bounded Contexts

## Overview

Five bounded contexts, one external domain, one anti-corruption layer.

```
┌─────────────────────────────────────────────────────────────────┐
│                     BUNZO QCOM DOMAIN                           │
│                                                                 │
│  ┌──────────────┐     ┌──────────────┐     ┌───────────────┐   │
│  │  DE Identity │────▶│   Trip       │────▶│   Payout      │   │
│  │  & Duty      │     │   Execution  │     │   & Earnings  │   │
│  └──────┬───────┘     └──────┬───────┘     └───────────────┘   │
│         │                   │                      ▲           │
│         │            ┌──────▼───────┐              │           │
│         └───────────▶│  Assignment  │──────────────┘           │
│                      │  (Process)   │                           │
│                      └──────────────┘                           │
│                                                                 │
│  ┌──────────────┐     ┌──────────────┐                         │
│  │   Referral   │────▶│   Payout     │  (bonus trigger)        │
│  │              │     │   & Earnings │                         │
│  └──────────────┘     └──────────────┘                         │
│                                                                 │
│  ┌──────────────┐                                               │
│  │   Customer   │  (read-only view of Trip Execution)          │
│  │   Tracking   │                                               │
│  └──────────────┘                                               │
└─────────────────────────────────────────────────────────────────┘
         │
         │  Anti-Corruption Layer (JavaOrderClient)
         ▼
┌─────────────────┐
│  Order Context  │  (External — Java order-service)
│  (Java / MySQL) │
└─────────────────┘
```

---

## 1. DE Identity & Duty Context

**Responsibility:** Who a Delivery Executive is and whether they are available to work.

**Ubiquitous Language:**
| Term | Meaning |
|---|---|
| Delivery Executive (DE) | A gig-based driver on the platform |
| Duty | An active work session at a darkstore |
| Status | Where in the work lifecycle the DE is (offline / eligible / busy / free) |
| Eligible | On duty at a darkstore, available for assignment |
| Busy | Currently executing a trip |
| Free | Trip completed, back at store, not yet re-eligible |
| QR Code | Time-bound store token that proves physical presence at a darkstore |
| Darkstore | A Bunzo fulfillment hub the DE is stationed at |
| Duty Index Key | `DE_ELIGIBLE#{storeId}` — the GSI key that makes the DE discoverable by the assignment cron |

**Aggregate Root:** `DeliveryExecutive`

**State Machine:**
```
offline ──(scan QR)──▶ eligible ──(cron assigns)──▶ busy ──(trip complete)──▶ free
   ▲                      │                                                      │
   └──────(duty/end)──────┘◀─────────────────────────(scan QR)──────────────────┘
```

**Domain Events (produced):**
- `DutyStarted` — DE scanned QR, now eligible
- `DutyEnded` — DE went offline
- `DEAssigned` — cron moved DE to busy
- `DEFreed` — trip completed, DE moved to free

**Owns:** `DE!{phone}` DynamoDB items

**Does NOT own:** Trip assignment logic (that's Assignment context), payout balances (that's Payout context)

---

## 2. Trip Execution Context

**Responsibility:** The lifecycle of a single delivery from warehouse to customer door.

**Ubiquitous Language:**
| Term | Meaning |
|---|---|
| Trip | One end-to-end delivery: darkstore → customer |
| Task | A discrete stop on a trip (pickup or drop) |
| Pickup Task | The darkstore stop — DE collects the order |
| Drop Task | The customer stop — DE delivers the order |
| Arrived | Pickup task auto-state: DE is at the darkstore when assigned |
| Reached | Drop task state: DE has verified OTP at customer door |
| OTP | 4-digit code shown to customer, verified by DE at doorstep |
| In Transit | Trip state after pickup complete — DE is heading to customer |
| Finding Driver | Pre-assignment state shown on customer track screen |

**Aggregate Root:** `Trip` (with `[]Task` embedded — tasks are not independent entities)

**Invariants enforced by this context:**
- A trip can only be assigned to one DE (DynamoDB transaction)
- Tasks progress forward only — no backward transitions
- Drop task cannot advance until pickup task is completed
- `arrived` on pickup is set by the system (cron), never by the DE via API

**State Machine — Trip:**
```
created ──(cron assigns)──▶ assigned ──(pickup done)──▶ in_transit
    ──(OTP verified)──▶ reached ──(drop done)──▶ completed
    (any active state) ──(cancellation detected)──▶ cancelled
```

**State Machine — Pickup Task:**
```
created ──(auto at assignment)──▶ arrived ──(DE taps done)──▶ completed
```

**State Machine — Drop Task:**
```
created ──(correct OTP)──▶ reached ──(DE taps done)──▶ completed
```

**Domain Events (produced):**
- `TripCreated` — cron created a trip for a READY_FOR_DELIVERY order
- `TripAssigned` — cron matched a DE to the trip
- `PickupCompleted` — DE tapped "Pickup Done" → notifies Order Context (OUT_FOR_DELIVERY)
- `OTPVerified` — DE entered correct OTP at customer door
- `TripCompleted` — drop task done → notifies Order Context (DELIVERED), triggers Payout
- `TripCancelled` — order cancelled mid-trip, DE freed

**Owns:** `TRIP!{tripId}` DynamoDB items, `OrderIndex` GSI, `DETripsIndex` GSI

---

## 3. Assignment Context (Orchestration Process)

**Responsibility:** Continuously match unassigned trips with eligible DEs. This is a process, not a rich domain — it has no entities of its own, only coordination logic.

**Ubiquitous Language:**
| Term | Meaning |
|---|---|
| Tick | One 10-second cron execution |
| Distributed Lock | DynamoDB item preventing parallel ticks across EC2 instances |
| FIFO | Assignment order: oldest unassigned trip gets oldest eligible DE |
| Assignment Conflict | DynamoDB transaction failure when DE or trip already taken |
| Cancellation Detection | Cron spotting a Java order that is no longer READY_FOR_DELIVERY |

**This context reads from:**
- Order Context (via ACL) — READY_FOR_DELIVERY orders
- DE Identity Context — eligible DEs by store

**This context writes to:**
- Trip Execution Context — creates trips, assigns DEs
- DE Identity Context — transitions DE to busy

**Owns:** `CRON_LOCK` DynamoDB items only

**Key design decisions embedded here:**
- Single hardcoded store `112233` (multi-store is future work)
- Google Maps Distance Matrix called in parallel for all new trips per tick
- Skip-and-retry on Maps failure (not abort)

---

## 4. Payout & Earnings Context

**Responsibility:** Computing what a DE earns per delivery, tracking all earnings over time, and recording when they get paid.

**Ubiquitous Language:**
| Term | Meaning |
|---|---|
| Base Pay | Distance-based earning per trip: `rate_per_km_zmw × distance_km` |
| Tier Bonus | Extra per-delivery bonus once daily delivery count crosses a threshold |
| Daily Rank | Which delivery of the day this is for the DE (1-indexed, resets at midnight Zambia) |
| Weekly Bonus | Consistency reward for working ≥ N days in a week |
| Earnings Ledger | Immutable append-only log of all earning events for a DE |
| Outstanding Balance | Sum of all earnings since the last disbursement |
| Disbursement | An offline cash/bank payment from Bunzo to a DE — recorded but not transferred digitally |
| ZMW | Zambian Kwacha — the currency unit, always in field names (`amount_zmw`) |

**Aggregates:**
- `EarningsLedger` (per DE) — all earning events, the source of truth for balance
- `DEWeeklySummary` (per DE per week) — computed once, idempotent
- `Disbursement` (per DE) — records of past payouts
- `PayoutConfig` — ops-controlled rate variables (global singleton)

**Domain Events (consumed):**
- `TripCompleted` → compute and record `trip` ledger entry
- `ReferralCompleted` → record `referral_bonus` ledger entries for both DEs
- Weekly cron fires → record `weekly_bonus` ledger entries for qualifying DEs

**Earning type hierarchy:**
```
Earning
├── Live Order Earning (type: trip)
│     base_pay_zmw + bonus_pay_zmw
└── Bonus
      ├── Tier Bonus (embedded in trip earning, not a separate ledger entry)
      ├── Weekly Bonus (type: weekly_bonus)
      └── Referral Bonus (type: referral_bonus)
```

**Configurable variables (PayoutConfig):**
```
rate_per_km_zmw         base rate per km
tier1_threshold         daily deliveries before tier 1 unlocks
tier1_bonus_zmw         bonus per delivery in tier 1
tier2_threshold         daily deliveries before tier 2 unlocks
tier2_bonus_zmw         bonus per delivery in tier 2
min_deliveries_per_day  minimum trips for a day to count as "worked"
weekly_w1/w2/w3_days    days worked thresholds for weekly bonuses
weekly_w1/w2/w3_bonus   weekly bonus amounts
referral_trips_threshold trips referred DE must complete for bonus
referral_window_days    window from registration for referred DE
referral_bonus_zmw      bonus for both DEs on referral completion
```

**Owns:** `EARN!{deId}`, `WEEKLY!{deId}`, `DISBURSEMENT!{deId}`, `CONFIG` DynamoDB items

---

## 5. Referral Context

**Responsibility:** Linking DEs via referral codes and crediting both parties when the referred DE hits the trip threshold.

**Ubiquitous Language:**
| Term | Meaning |
|---|---|
| Referral Code | Static 6-digit numeric code unique to each DE |
| Referrer | The DE who shared their code |
| Referred DE | The DE who used a code during registration |
| Window | Time period (from referred DE registration) within which threshold must be hit |
| Threshold | Number of trips the referred DE must complete to trigger bonus |
| Bonus Trigger | Inline check at trip completion — idempotent via conditional DynamoDB write |

**Aggregate Root:** `Referral` (keyed on referred DE — one referrer per referred DE)

**Invariants:**
- A referred DE can only have one referrer (enforced by `attribute_not_exists(PK)`)
- A referral code is entered only at registration time — never updated
- Referral bonus is triggered exactly once (conditional write: `status = active`)
- Window expiry is lazy — checked inline, no background job

**Owns:** `REFERRAL!{referredDeId}` DynamoDB items, `ReferralCodeIndex` GSI on DE table

---

## 6. Customer Tracking Context (Read Model)

**Responsibility:** A customer-facing read model over Trip Execution. No writes, no business logic — it assembles a view.

**Ubiquitous Language:**
| Term | Meaning |
|---|---|
| Finding Driver | No trip yet, or unassigned trip |
| ETA | 15-minute countdown from trip creation (Zambia timezone) |
| Delayed | ETA elapsed — show support message |
| OTP | Shown to customer from drop task; hidden once drop task is `reached` |

**This context reads from:**
- Trip Execution Context — trip + task state
- DE Identity Context — DE name
- Order Context (ACL) — verify order exists when no trip yet

**Returns 400 (not 200) for:** completed trips (`TRIP_COMPLETED`), cancelled trips (`TRIP_CANCELLED`)

**Owns:** Nothing — pure read model, no DynamoDB writes

---

## 7. Order Context (External)

**Owner:** Java order-service (separate codebase, MySQL)

**Anti-Corruption Layer:** `JavaOrderClient` in this codebase translates between Java's order model and our domain language.

**Translation table:**
| Java Order Status | Our Domain Event |
|---|---|
| `READY_FOR_DELIVERY` | Order needs a trip (Assignment context reads this) |
| `OUT_FOR_DELIVERY` | PickupCompleted (we write this to Java) |
| `DELIVERED` | TripCompleted (we write this to Java) |
| `CANCELLED` | TripCancelled (cron detects this) |

**Integration pattern:** Polling (cron reads Java) + async push (goroutine writes to Java on task completion with retry).

---

## Context Map

```
DE Identity ──────────────────────────────────────────────────────────────┐
    │ (DE status, eligible DEs)                                            │
    ▼                                                                      │
Assignment ◀──── Order Context (ACL: JavaOrderClient) ◀── Java orders     │
    │                                                                      │
    │ creates trips + assigns DEs                                          │
    ▼                                                                      │
Trip Execution ──────────────────────────── writes back ──▶ Order Context │
    │ TripCompleted                                                        │
    ├──▶ Payout & Earnings (writes ledger entry)                          │
    │                                                                      │
    └──▶ Referral (CheckAndTriggerBonus)                                  │
              │ ReferralCompleted                                          │
              └──▶ Payout & Earnings (writes referral_bonus entry)        │
                                                                           │
Customer Tracking ◀──── Trip Execution (read) ◀──────────────────────────┘
                  ◀──── DE Identity (read: DE name)
                  ◀──── Order Context (verify order exists via ACL)
```

**Relationship types:**
- Assignment → Trip Execution: **Conformist** (Assignment writes directly into Trip's schema)
- Assignment → Order Context: **Anti-Corruption Layer** (JavaOrderClient translates)
- Trip Execution → Payout: **Published Language** (TripCompleted event carries payout fields)
- Referral → Payout: **Customer-Supplier** (Referral triggers, Payout records)
- Customer Tracking → Trip Execution: **Open Host Service** (read-only via repository)
