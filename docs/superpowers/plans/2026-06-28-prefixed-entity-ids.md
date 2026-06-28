# Prefixed Entity IDs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace random UUID entity IDs in qcom with human-readable, type-prefixed, fixed-width IDs (e.g. `TR0458047115`) generated from per-type atomic DynamoDB counters + an Optimus reversible encoding.

**Architecture:** A new `internal/ids` package owns the Optimus codec and a `Generator` backed by an atomic DynamoDB counter (one `COUNTER!<TYPE>` item per entity type). ID assignment moves to the **repository write boundary**: each repo's create/write method assigns the ID (when empty) just before persisting, since repos already hold `(*dynamodb.Client, tableName)` and return errors. New records only; existing UUIDs coexist.

**Tech Stack:** Go 1.23, aws-sdk-go-v2 (`github.com/aws/aws-sdk-go-v2/service/dynamodb`), DynamoDB single-table (`QComTable`), logrus.

## Global Constraints

- **Format:** `<2-letter prefix>` + exactly **10 zero-padded digits** = 12 chars (e.g. `TR0458047115`). Always fixed width.
- **Optimus constants (ported verbatim from `inventory/.../OrderNumbers.java`, ONE shared set):** `prime=1580030173`, `inverse=59260789`, `xor=1163945558`, `maxID=(1<<31)-1`, `digitWidth=10`. Encode: `((n*prime) & maxID) ^ xor`. Decode: `((e ^ xor) * inverse) & maxID`.
- **Known vectors (must hold):** `Trip.Format(1) == "TR0458047115"`, `Trip.Format(2) == "TR2033899500"`.
- **Entity → prefix / counter key:** User `US`/`COUNTER!USER`; DE `DE`/`COUNTER!DE`; Trip `TR`/`COUNTER!TRIP`; Task `TK`/`COUNTER!TASK`; Address `AD`/`COUNTER!ADDRESS`; Dispute `DP`/`COUNTER!DISPUTE`; EarningsLedger `EA`/`COUNTER!EARNING`; Disbursement `DB`/`COUNTER!DISBURSEMENT`; CashDeposit `CD`/`COUNTER!DEPOSIT`.
- **Counter item:** `PK=COUNTER!<TYPE>`, `SK=METADATA`, attribute `seq` (Number). Increment via `UpdateExpression: "ADD seq :one"` with `ReturnValues: UPDATED_NEW`; starts at 1; gaps acceptable.
- **Fail-closed:** if the counter call fails, propagate the error (operation/HTTP fails; void/best-effort sites log-and-skip exactly as they do today for other write errors). NEVER silently fall back to `uuid.New()`.
- **Generate-if-empty:** a repo write method assigns an ID only when the field is empty (preserves client-supplied idempotency keys, e.g. cash deposit, and keeps direct unit tests that pass explicit IDs working).
- **Scope:** qcom only. Do NOT touch inventory/MySQL, `order_uuid`, `order_number`, or reference fields (`Trip.order_id`, `Trip.store_id`, `EarningsLedger.ReferenceID`). Do NOT migrate existing records.
- **Excluded entities (leave as-is):** CallRecord, referral_code, AdminUser, Rule, Darkstore.
- **Testing reality:** integration tests (`//go:build integration`) require Docker + DynamoDB Local and are ALREADY BROKEN in baseline (stale `NewUploadService`) — do NOT run or fix them. The verification gate for every task is: `go build ./...`, `go test ./...` (unit, default tags), and `go vet ./internal/... ./cmd/...`. The `internal/ids` package carries the real behavioral coverage via unit tests with a fake counter.

---

## File Structure

- **Create** `internal/ids/ids.go` — codec, `EntityType` table, `Counter` interface, `Generator`, `dynamoCounter` (prod impl).
- **Create** `internal/ids/ids_test.go` — unit tests (codec round-trip, known vectors, range, format, prefix uniqueness, generator with fake counter).
- **Modify** repositories to assign IDs at write time: `user_repository.go`, `de_repository.go` (`Create` + `ApplyCashDeposit`), `trip_repository.go`, `address_repository.go`, `dispute_repository.go`, `earnings_ledger_repository.go`, `disbursement_repository.go`. Each gains an `idGen *ids.Generator` field initialized in its constructor from the existing `client`+`tableName` (no constructor signature change, no `main.go` change).
- **Modify** call sites to STOP generating UUIDs: `de_service.go`, `assignment_cron.go`, `address_service.go`, `dispute_service.go`, `payout_service.go`, `reward_engine.go`, `cash_deposit_service.go`, `disbursement_handlers.go`.

---

## Task 1: `internal/ids` package (codec + generator)

**Files:**
- Create: `internal/ids/ids.go`
- Test: `internal/ids/ids_test.go`

**Interfaces:**
- Produces (consumed by all later tasks):
  - `type EntityType struct { Prefix string; CounterKey string }`
  - Exported vars: `ids.User, ids.DE, ids.Trip, ids.Task, ids.Address, ids.Dispute, ids.Earning, ids.Disbursement, ids.CashDeposit`
  - `func (t EntityType) Format(n int64) (string, error)`
  - `func (t EntityType) Decode(id string) (int64, error)`
  - `type Counter interface { NextValue(ctx context.Context, counterKey string) (int64, error) }`
  - `type Generator struct{ ... }`
  - `func NewGenerator(client *dynamodb.Client, tableName string) *Generator`
  - `func NewGeneratorWithCounter(c Counter) *Generator`
  - `func (g *Generator) NextID(ctx context.Context, t EntityType) (string, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/ids/ids_test.go`:

```go
package ids

import (
	"context"
	"errors"
	"regexp"
	"testing"
)

func TestFormatKnownVectors(t *testing.T) {
	cases := map[int64]string{1: "TR0458047115", 2: "TR2033899500"}
	for n, want := range cases {
		got, err := Trip.Format(n)
		if err != nil {
			t.Fatalf("Format(%d) error: %v", n, err)
		}
		if got != want {
			t.Fatalf("Trip.Format(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatShape(t *testing.T) {
	re := regexp.MustCompile(`^[A-Z]{2}\d{10}$`)
	for _, et := range allTypes() {
		got, err := et.Format(12345)
		if err != nil {
			t.Fatalf("%s Format error: %v", et.Prefix, err)
		}
		if !re.MatchString(got) {
			t.Fatalf("%s Format(12345) = %q, not 2-alpha + 10-digit", et.Prefix, got)
		}
	}
}

func TestFormatRange(t *testing.T) {
	for _, n := range []int64{0, -1, maxID + 1} {
		if _, err := Trip.Format(n); err == nil {
			t.Fatalf("Format(%d) expected error, got nil", n)
		}
	}
	if _, err := Trip.Format(maxID); err != nil {
		t.Fatalf("Format(maxID) unexpected error: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	ns := []int64{1, 2, 3, 100, 999999, 1000000000, maxID - 1, maxID}
	for _, n := range ns {
		id, err := Trip.Format(n)
		if err != nil {
			t.Fatalf("Format(%d): %v", n, err)
		}
		back, err := Trip.Decode(id)
		if err != nil {
			t.Fatalf("Decode(%q): %v", id, err)
		}
		if back != n {
			t.Fatalf("round-trip n=%d -> %q -> %d", n, id, back)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	bad := []string{"", "TR", "TRABCDEFGHIJ", "TR123", "XX0458047115", "TR04580471150"}
	for _, s := range bad {
		if _, err := Trip.Decode(s); err == nil {
			t.Fatalf("Decode(%q) expected error", s)
		}
	}
	// Cross-prefix: a Trip ID must not decode as a User ID.
	tripID, _ := Trip.Format(5)
	if _, err := User.Decode(tripID); err == nil {
		t.Fatalf("User.Decode(%q) should fail (wrong prefix)", tripID)
	}
}

func TestPrefixesAndKeysUnique(t *testing.T) {
	seenP, seenK := map[string]bool{}, map[string]bool{}
	for _, et := range allTypes() {
		if len(et.Prefix) != 2 {
			t.Fatalf("prefix %q not 2 chars", et.Prefix)
		}
		if seenP[et.Prefix] {
			t.Fatalf("duplicate prefix %q", et.Prefix)
		}
		if seenK[et.CounterKey] {
			t.Fatalf("duplicate counter key %q", et.CounterKey)
		}
		seenP[et.Prefix], seenK[et.CounterKey] = true, true
	}
}

type fakeCounter struct {
	n   int64
	err error
}

func (f *fakeCounter) NextValue(_ context.Context, _ string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.n++
	return f.n, nil
}

func TestGeneratorNextID(t *testing.T) {
	g := NewGeneratorWithCounter(&fakeCounter{})
	first, err := g.NextID(context.Background(), Trip)
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if first != "TR0458047115" {
		t.Fatalf("first NextID = %q, want TR0458047115", first)
	}
	second, _ := g.NextID(context.Background(), Trip)
	if second != "TR2033899500" {
		t.Fatalf("second NextID = %q, want TR2033899500", second)
	}
}

func TestGeneratorPropagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	g := NewGeneratorWithCounter(&fakeCounter{err: sentinel})
	if _, err := g.NextID(context.Background(), Trip); err == nil {
		t.Fatalf("expected error from counter")
	}
}

func allTypes() []EntityType {
	return []EntityType{User, DE, Trip, Task, Address, Dispute, Earning, Disbursement, CashDeposit}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ids/...`
Expected: FAIL — `internal/ids` package does not exist / undefined symbols.

- [ ] **Step 3: Write the implementation**

Create `internal/ids/ids.go`:

```go
// Package ids generates prefixed, fixed-width entity identifiers backed by
// per-type atomic DynamoDB counters and an Optimus reversible encoding.
package ids

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Optimus constants — ported verbatim from the Java order-service
// (OrderNumbers.java). One shared set; the 2-letter prefix namespaces values
// so reuse across entity types is safe.
const (
	optimusPrime   int64 = 1580030173
	optimusInverse int64 = 59260789
	optimusXor     int64 = 1163945558
	maxID          int64 = (1 << 31) - 1
	digitWidth           = 10
)

// EntityType binds an entity to its 2-letter ID prefix and DynamoDB counter key.
type EntityType struct {
	Prefix     string
	CounterKey string
}

var (
	User         = EntityType{Prefix: "US", CounterKey: "COUNTER!USER"}
	DE           = EntityType{Prefix: "DE", CounterKey: "COUNTER!DE"}
	Trip         = EntityType{Prefix: "TR", CounterKey: "COUNTER!TRIP"}
	Task         = EntityType{Prefix: "TK", CounterKey: "COUNTER!TASK"}
	Address      = EntityType{Prefix: "AD", CounterKey: "COUNTER!ADDRESS"}
	Dispute      = EntityType{Prefix: "DP", CounterKey: "COUNTER!DISPUTE"}
	Earning      = EntityType{Prefix: "EA", CounterKey: "COUNTER!EARNING"}
	Disbursement = EntityType{Prefix: "DB", CounterKey: "COUNTER!DISBURSEMENT"}
	CashDeposit  = EntityType{Prefix: "CD", CounterKey: "COUNTER!DEPOSIT"}
)

func encodeOptimus(n int64) int64 { return ((n * optimusPrime) & maxID) ^ optimusXor }
func decodeOptimus(e int64) int64 { return ((e ^ optimusXor) * optimusInverse) & maxID }

// Format builds the prefixed, zero-padded ID for counter value n (1..maxID).
func (t EntityType) Format(n int64) (string, error) {
	if n <= 0 || n > maxID {
		return "", fmt.Errorf("ids: counter value out of range: %d", n)
	}
	return fmt.Sprintf("%s%0*d", t.Prefix, digitWidth, encodeOptimus(n)), nil
}

// Decode reverses an ID produced by Format back to its counter value, after
// validating prefix, width, numeric payload, range, and round-trip.
func (t EntityType) Decode(id string) (int64, error) {
	if !strings.HasPrefix(id, t.Prefix) {
		return 0, fmt.Errorf("ids: %q missing prefix %q", id, t.Prefix)
	}
	digits := id[len(t.Prefix):]
	if len(digits) != digitWidth {
		return 0, fmt.Errorf("ids: %q has wrong digit width", id)
	}
	encoded, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ids: %q non-numeric payload: %w", id, err)
	}
	n := decodeOptimus(encoded)
	if n <= 0 || n > maxID {
		return 0, fmt.Errorf("ids: %q decodes out of range", id)
	}
	if round, _ := t.Format(n); round != id {
		return 0, fmt.Errorf("ids: %q failed round-trip", id)
	}
	return n, nil
}

// Counter yields the next monotonic integer for a counter key.
type Counter interface {
	NextValue(ctx context.Context, counterKey string) (int64, error)
}

// Generator produces prefixed IDs from a Counter.
type Generator struct{ counter Counter }

// NewGenerator builds a Generator backed by an atomic DynamoDB counter.
func NewGenerator(client *dynamodb.Client, tableName string) *Generator {
	return &Generator{counter: dynamoCounter{client: client, tableName: tableName}}
}

// NewGeneratorWithCounter injects a custom Counter (for tests).
func NewGeneratorWithCounter(c Counter) *Generator { return &Generator{counter: c} }

// NextID returns the next prefixed ID for t, or an error if the counter fails.
func (g *Generator) NextID(ctx context.Context, t EntityType) (string, error) {
	n, err := g.counter.NextValue(ctx, t.CounterKey)
	if err != nil {
		return "", fmt.Errorf("ids: counter %s: %w", t.CounterKey, err)
	}
	return t.Format(n)
}

// dynamoCounter implements Counter via an atomic ADD on a single counter item.
type dynamoCounter struct {
	client    *dynamodb.Client
	tableName string
}

func (d dynamoCounter) NextValue(ctx context.Context, counterKey string) (int64, error) {
	out, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: counterKey},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String("ADD seq :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":one": &types.AttributeValueMemberN{Value: "1"}},
		ReturnValues:              types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, err
	}
	attr, ok := out.Attributes["seq"].(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("ids: counter %s missing seq attribute", counterKey)
	}
	n, err := strconv.ParseInt(attr.Value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ids: counter %s seq parse: %w", counterKey, err)
	}
	return n, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ids/... -v`
Expected: PASS (all tests). Then `go build ./...` and `go vet ./internal/ids/...` clean.

- [ ] **Step 5: Commit**

```bash
git add internal/ids/
git commit -m "feat(ids): add prefixed entity ID generator (Optimus + DynamoDB counter)"
```

---

## Task 2: Wire ID generation into simple PutItem repos (User, Address, DE)

**Files:**
- Modify: `internal/repository/user_repository.go`
- Modify: `internal/repository/address_repository.go`
- Modify: `internal/repository/de_repository.go`
- Modify: `internal/service/address_service.go:41` (remove uuid)
- Modify: `internal/service/de_service.go:52` (remove uuid)

**Interfaces:**
- Consumes: `ids.NewGenerator`, `(*Generator).NextID`, `ids.User`, `ids.Address`, `ids.DE` (Task 1).
- Produces: the `idGen *ids.Generator` repo-field pattern reused by later tasks; in particular `DERepository` gains `idGen` here (Task 7 reuses it).

**Pattern (apply to each repo struct + constructor):** add field `idGen *ids.Generator`, initialize in the `New…` constructor as `idGen: ids.NewGenerator(client, tableName)`. Import `github.com/qcom/qcom/internal/ids`. Remove the now-unused `github.com/google/uuid` import from any service file that no longer references it.

- [ ] **Step 1: UserRepository — generate `user_id` in `Create`**

In `internal/repository/user_repository.go`, add the field + constructor init, then replace the generation line.

Struct/constructor (add `idGen`):

```go
type UserRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
	idGen     *ids.Generator
}

func NewUserRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *UserRepository {
	return &UserRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
		idGen:     ids.NewGenerator(client, tableName),
	}
}
```

In `Create`, replace:

```go
	now := time.Now()
	user.UserID = uuid.New().String()
```

with:

```go
	now := time.Now()
	if user.UserID == "" {
		id, err := r.idGen.NextID(ctx, ids.User)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate user_id: %w", err))
		}
		user.UserID = id
	}
```

Remove the `github.com/google/uuid` import from `user_repository.go` if no longer used.

- [ ] **Step 2: AddressRepository — generate `address_id` in `Create`; remove from service**

In `internal/repository/address_repository.go`, add the `idGen` field + constructor init (same pattern). At the top of `Create`, before `address.GetPK()` is used, insert:

```go
	if address.AddressID == "" {
		id, err := r.idGen.NextID(ctx, ids.Address)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate address_id: %w", err))
		}
		address.AddressID = id
	}
```

(If `Create` has no `op` logging var, return the wrapped error directly.) Then in `internal/service/address_service.go`, delete the line `addr.AddressID = uuid.New().String()` and remove the `uuid` import if unused.

- [ ] **Step 3: DERepository — generate `de_id` in `Create`; remove from service**

In `internal/repository/de_repository.go`, add the `idGen` field + constructor init. In `Create`, after the timestamps and before `attributevalue.MarshalMap(de)`, insert:

```go
	if de.DEID == "" {
		id, err := r.idGen.NextID(ctx, ids.DE)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate de_id: %w", err))
		}
		de.DEID = id
	}
```

In `internal/service/de_service.go`, change the struct literal so `DEID` is no longer set from uuid:

```go
	de := &models.DeliveryExecutive{
		PhoneNumber:      req.PhoneNumber,
		Name:             req.Name,
		ProfileURL:       req.ProfileURL,
		NRCURL:           req.NRCURL,
		DriverLicenseURL: req.DriverLicenseURL,
		Status:           models.DEStatusOffline,
		ReferralCode:     referralCode,
	}
```

Remove the `uuid` import from `de_service.go` if unused. (Note: `de.DEID` is read AFTER `Create` for `LinkReferral` and the HTTP response — this works because `Create` mutates the same `*DeliveryExecutive` pointer in place. Do not reorder those reads before `Create`.)

- [ ] **Step 4: Run the suite**

Run: `go build ./... && go test ./... && go vet ./internal/... ./cmd/...`
Expected: PASS. If `de_service_test.go`, `address` handler/service tests, or user tests assert a non-empty/UUID ID at the service layer (they currently do not — `de_service_test.go` only checks the ledger reader's `gotDEID`), update them to reflect that IDs are now assigned at the repository boundary (fake repos used in those tests will leave the ID empty). Do not weaken assertions beyond what this contract change requires.

- [ ] **Step 5: Commit**

```bash
git add internal/repository/user_repository.go internal/repository/address_repository.go internal/repository/de_repository.go internal/service/address_service.go internal/service/de_service.go
git commit -m "feat(ids): assign user/address/de IDs at repository write boundary"
```

---

## Task 3: TripRepository — generate `trip_id` + task IDs

**Files:**
- Modify: `internal/repository/trip_repository.go`
- Modify: `internal/service/assignment_cron.go:399-401` (remove 3 uuid calls)

**Interfaces:**
- Consumes: `ids.NewGenerator`, `(*Generator).NextID`, `ids.Trip`, `ids.Task`.
- Produces: a fully-populated `*models.Trip` (TripID + every `Tasks[i].TaskID`) after `Create` returns.

- [ ] **Step 1: TripRepository — assign IDs before `GetPK`/marshal**

In `internal/repository/trip_repository.go`, add the `idGen *ids.Generator` field + constructor init (same pattern as Task 2). In `Create`, BEFORE `trip.SyncIndexKeys()` / `trip.GetPK()` (which depend on `TripID`), insert:

```go
	if trip.TripID == "" {
		id, err := r.idGen.NextID(ctx, ids.Trip)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate trip_id: %w", err))
		}
		trip.TripID = id
	}
	for i := range trip.Tasks {
		if trip.Tasks[i].TaskID == "" {
			tid, err := r.idGen.NextID(ctx, ids.Task)
			if err != nil {
				return op.Fail(fmt.Errorf("failed to generate task_id: %w", err))
			}
			trip.Tasks[i].TaskID = tid
		}
	}
```

(Match the existing error-return style in `Create` — use `op.Fail(...)` if present, else return the wrapped error.)

- [ ] **Step 2: assignment_cron — stop generating UUIDs**

In `internal/service/assignment_cron.go` `createTrip`, replace:

```go
	tripID := uuid.New().String()
	pickupTaskID := uuid.New().String()
	dropTaskID := uuid.New().String()
```

with:

```go
	// IDs are assigned by TripRepository.Create at the persistence boundary.
	var tripID, pickupTaskID, dropTaskID string
```

Leave `buildTripFromOrder(order, tripID, pickupTaskID, dropTaskID, ...)` unchanged (it now receives empty strings; the repo fills them). Remove the `uuid` import from `assignment_cron.go` if unused. Note: code AFTER `c.tripRepo.Create(...)` (assign loop, push notifications) reads `trip.TripID` — this is safe because `Create` populated it in place.

- [ ] **Step 3: Run the suite**

Run: `go build ./... && go test ./... && go vet ./internal/... ./cmd/...`
Expected: PASS. `assignment_cron_test.go` calls `buildTripFromOrder` directly with explicit IDs (`"trip-1"`, `"pick-1"`, `"drop-1"`) and the sort tests use literal `TripID`s — these remain valid. If any cron test asserts that `createTrip`'s returned trip has a non-empty TripID via a fake `tripRepo`, update that fake's `Create` to assign a deterministic stub ID (e.g. `"TR0000000001"`) to mimic the real boundary.

- [ ] **Step 4: Commit**

```bash
git add internal/repository/trip_repository.go internal/service/assignment_cron.go
git commit -m "feat(ids): assign trip and task IDs at TripRepository write boundary"
```

---

## Task 4: EarningsLedger — generate `earning_id` in `Append`

**Files:**
- Modify: `internal/repository/earnings_ledger_repository.go`
- Modify: `internal/service/payout_service.go:93,120` (remove 2 uuid calls)
- Modify: `internal/service/reward_engine.go:35,118` (remove 2 uuid calls)

**Interfaces:**
- Consumes: `ids.NewGenerator`, `(*Generator).NextID`, `ids.Earning`.
- Produces: every persisted `EarningsLedger` has a populated `earning_id`; pure builders in `reward_engine.go` now return entries with empty `EarningID` (filled on `Append`).

- [ ] **Step 1: EarningsLedgerRepository — assign `earning_id` before `GetSK`/marshal**

In `internal/repository/earnings_ledger_repository.go`, add the `idGen *ids.Generator` field + constructor init. In `Append`, AFTER the `entry.CreatedAt` default and BEFORE `attributevalue.MarshalMap(entry)` (SK = `{created_at}#{earning_id}` needs both), insert:

```go
	if entry.EarningID == "" {
		id, err := r.idGen.NextID(ctx, ids.Earning)
		if err != nil {
			return fmt.Errorf("failed to generate earning_id: %w", err)
		}
		entry.EarningID = id
	}
```

(Use the existing error style of `Append`; add `fmt` import if missing.)

- [ ] **Step 2: payout_service — stop generating UUIDs**

In `internal/service/payout_service.go`, delete `EarningID: uuid.New().String(),` from BOTH the trip-completion entry (~line 93) and the referral-bonus entry (~line 120). Remove the `uuid` import if unused. Behavior on counter failure: `Append` now returns the error and the existing best-effort logging in these void methods handles it exactly as it already handles other `Append` errors.

- [ ] **Step 3: reward_engine — stop generating UUIDs in pure builders**

In `internal/service/reward_engine.go`, delete `EarningID: uuid.New().String(),` from both `EvaluateAccumulator` (~line 35) and `EvaluateRanking` (~line 118). The functions stay pure and return entries with empty `EarningID`; `RewardCron.appendIdempotent` persists via `Append` which now assigns the ID. Its idempotency key is `(de_id, type, reference_id)` — NOT `earning_id` — so this is safe. Remove the `uuid` import if unused.

- [ ] **Step 4: Run the suite**

Run: `go build ./... && go test ./... && go vet ./internal/... ./cmd/...`
Expected: PASS. `reward_engine_test.go` has no `EarningID` assertions. If `reward_cron_test.go` or `earnings_handlers_test.go` assert a non-empty `earning_id` through a fake ledger repo, update the fake's `Append` to assign a deterministic stub ID, or adjust the assertion to the new boundary contract.

- [ ] **Step 5: Commit**

```bash
git add internal/repository/earnings_ledger_repository.go internal/service/payout_service.go internal/service/reward_engine.go
git commit -m "feat(ids): assign earning_id at EarningsLedger write boundary"
```

---

## Task 5: Disbursement (+ its mirror earning) at handler/repo boundary

**Files:**
- Modify: `internal/repository/disbursement_repository.go`
- Modify: `internal/handlers/disbursement_handlers.go:167,192` (remove 2 uuid calls)

**Depends on:** Task 4 (`EarningsLedgerRepository.Append` already assigns `earning_id`).

**Interfaces:**
- Consumes: `ids.NewGenerator`, `(*Generator).NextID`, `ids.Disbursement`; relies on Task 4 for the mirror earning's ID.
- Produces: `*models.Disbursement` with `DisbursementID` populated after `Create`, so the handler can set the mirror earning's `ReferenceID` and the JSON response.

- [ ] **Step 1: DisbursementRepository — assign `disbursement_id` before `GetSK`/marshal**

In `internal/repository/disbursement_repository.go`, add the `idGen *ids.Generator` field + constructor init. In `Create`, before `d.GetSK()`/marshal (SK = `{disbursed_at}#{disbursement_id}`), insert:

```go
	if d.DisbursementID == "" {
		id, err := r.idGen.NextID(ctx, ids.Disbursement)
		if err != nil {
			return fmt.Errorf("failed to generate disbursement_id: %w", err)
		}
		d.DisbursementID = id
	}
```

(Match the method's existing error/logging style; add `fmt` if needed.)

- [ ] **Step 2: disbursement_handlers — stop generating both UUIDs**

In `internal/handlers/disbursement_handlers.go` `RecordDisbursement`:
- Remove `DisbursementID: uuid.New().String(),` from the `disbursement` literal. The handler already calls `h.disbursementRepo.Create(r.Context(), disbursement)` and then reads `disbursement.DisbursementID` for the mirror's `ReferenceID` and the response — this works because `Create` populates it in place. Do NOT reorder: `Create` (disbursement) must run before the mirror is built.
- Remove `EarningID: uuid.New().String(),` from the `mirror` literal (Task 4's `Append` assigns it).
- Remove the `uuid` import if unused.
- On counter failure: `Create` returns an error and the handler's existing error path returns HTTP 500 (fail-closed).

- [ ] **Step 3: Run the suite**

Run: `go build ./... && go test ./... && go vet ./internal/... ./cmd/...`
Expected: PASS. `disbursement_handlers_test.go` likely asserts the response `disbursement_id`. Since the handler no longer assigns it (the repo does), update the test's fake `disbursementRepo.Create` to assign a deterministic stub ID (e.g. `d.DisbursementID = "DB0000000001"`) so the mirror `ReferenceID` and response assertions hold. Mirror the real in-place-mutation contract; do not delete the cross-reference assertion.

- [ ] **Step 4: Commit**

```bash
git add internal/repository/disbursement_repository.go internal/handlers/disbursement_handlers.go
git commit -m "feat(ids): assign disbursement_id at write boundary; drop handler UUIDs"
```

---

## Task 6: DisputeRepository — generate `dispute_id` in `Create`

**Files:**
- Modify: `internal/repository/dispute_repository.go`
- Modify: `internal/service/dispute_service.go:144` (remove uuid)

**Interfaces:**
- Consumes: `ids.NewGenerator`, `(*Generator).NextID`, `ids.Dispute`.
- Produces: `*models.Dispute` with `DisputeID` populated after `Create` (notifier + HTTP DTO read it afterward).

- [ ] **Step 1: DisputeRepository — assign `dispute_id` before building the transaction items**

In `internal/repository/dispute_repository.go`, add the `idGen *ids.Generator` field + constructor init. In `Create`, BEFORE `d.GetPK()` and before the `TransactWriteItems` items are built (the guard item embeds `d.DisputeID`), insert:

```go
	if d.DisputeID == "" {
		id, err := r.idGen.NextID(ctx, ids.Dispute)
		if err != nil {
			return fmt.Errorf("failed to generate dispute_id: %w", err)
		}
		d.DisputeID = id
	}
```

(Add `fmt` import if needed.)

- [ ] **Step 2: dispute_service — stop generating UUID**

In `internal/service/dispute_service.go`, remove `DisputeID: uuid.New().String(),` from the `d := &models.Dispute{...}` literal. The notifier and HTTP DTO read `d.DisputeID` AFTER `s.disputes.Create(ctx, d)` returns — safe via in-place mutation. Remove the `uuid` import if unused.

- [ ] **Step 3: Run the suite**

Run: `go build ./... && go test ./... && go vet ./internal/... ./cmd/...`
Expected: PASS. If `dispute_service_test.go` / `dispute_notifier_test.go` / `admin_dispute_service_test.go` assert a non-empty `dispute_id` via a fake `disputeStore`, update that fake's `Create` to assign a deterministic stub ID to mimic the boundary.

- [ ] **Step 4: Commit**

```bash
git add internal/repository/dispute_repository.go internal/service/dispute_service.go
git commit -m "feat(ids): assign dispute_id at DisputeRepository write boundary"
```

---

## Task 7: CashDeposit — generate `deposit_id` in `DERepository.ApplyCashDeposit`

**Files:**
- Modify: `internal/repository/de_repository.go` (`ApplyCashDeposit`; the `idGen` field already exists from Task 2)
- Modify: `internal/service/cash_deposit_service.go:79` (remove uuid fallback)

**Interfaces:**
- Consumes: `ids.CashDeposit`, the `idGen` field added to `DERepository` in Task 2.
- Produces: persisted `CashDepositLedger` with `deposit_id` populated, preserving client-supplied idempotency keys.

- [ ] **Step 1: ApplyCashDeposit — generate `deposit_id` only when empty**

In `internal/repository/de_repository.go` `ApplyCashDeposit`, BEFORE `attributevalue.MarshalMap(entry)` / `entry.GetSK()` (SK = `deposit_id`), insert:

```go
	if entry.DepositID == "" {
		id, err := r.idGen.NextID(ctx, ids.CashDeposit)
		if err != nil {
			return fmt.Errorf("failed to generate deposit_id: %w", err)
		}
		entry.DepositID = id
	}
```

This preserves the idempotency contract: a client-supplied `deposit_id` (non-empty) is kept; only an empty one is generated.

- [ ] **Step 2: cash_deposit_service — remove the uuid fallback**

In `internal/service/cash_deposit_service.go` `RecordDeposit`, delete:

```go
	if depositID == "" {
		depositID = uuid.New().String()
	}
```

The service still builds `entry` with `DepositID: depositID` (possibly empty); the repo now generates when empty. Remove the `uuid` import if unused.

- [ ] **Step 3: Run the suite**

Run: `go build ./... && go test ./... && go vet ./internal/... ./cmd/...`
Expected: PASS. If `cash_limit` unit tests (non-integration) assert a generated `deposit_id`, adjust to the boundary contract; client-supplied-ID paths are unchanged.

- [ ] **Step 4: Commit**

```bash
git add internal/repository/de_repository.go internal/service/cash_deposit_service.go
git commit -m "feat(ids): assign deposit_id in ApplyCashDeposit, preserve idempotency key"
```

---

## Done criteria

- `go build ./...`, `go test ./...`, and `go vet ./internal/... ./cmd/...` all clean.
- No remaining `uuid.New()` for the 9 in-scope entities (verify: `grep -rn "uuid.New()" internal/ | grep -iE "user_id|de_id|trip_id|task_id|address_id|dispute_id|earning_id|disbursement_id|deposit_id"` returns nothing relevant). Excluded entities (CallRecord, etc.) and `jti`/`familyID`/trace IDs may still use uuid — that is correct.
- All in-scope IDs are produced via `ids.Generator.NextID` at the repository write boundary, fail-closed on counter errors.
