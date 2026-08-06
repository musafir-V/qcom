package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// fakeRefreshStore is a thread-safe in-memory refreshTokenStore for Rotate tests.
type fakeRefreshStore struct {
	mu           sync.Mutex
	tokens       map[string]models.RefreshTokenData
	revoked      map[string]time.Time
	replacements map[string]models.RefreshReplacement
}

func newFakeRefreshStore() *fakeRefreshStore {
	return &fakeRefreshStore{
		tokens:       make(map[string]models.RefreshTokenData),
		revoked:      make(map[string]time.Time),
		replacements: make(map[string]models.RefreshReplacement),
	}
}

func (f *fakeRefreshStore) Store(ctx context.Context, tokenData models.RefreshTokenData) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[tokenData.JTI] = tokenData
	return nil
}

func (f *fakeRefreshStore) Get(ctx context.Context, jti string) (*models.RefreshTokenData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[jti]
	if !ok {
		return nil, fmt.Errorf("refresh token not found")
	}
	cp := t
	return &cp, nil
}

func (f *fakeRefreshStore) IsRevoked(ctx context.Context, jti string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.revoked[jti]
	return ok, nil
}

func (f *fakeRefreshStore) MarkRevoked(ctx context.Context, jti string, expiresAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[jti] = expiresAt
	return nil
}

func (f *fakeRefreshStore) TryMarkRevoked(ctx context.Context, jti string, expiresAt time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.revoked[jti]; exists {
		return false, nil
	}
	f.revoked[jti] = expiresAt
	return true, nil
}

func (f *fakeRefreshStore) StoreReplacement(ctx context.Context, rep models.RefreshReplacement, graceTTL time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replacements[rep.OldJTI] = rep
	return nil
}

func (f *fakeRefreshStore) GetReplacement(ctx context.Context, oldJTI string) (*models.RefreshReplacement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rep, ok := f.replacements[oldJTI]
	if !ok {
		return nil, nil
	}
	cp := rep
	return &cp, nil
}

func (f *fakeRefreshStore) GetByFamilyID(ctx context.Context, familyID string) ([]models.RefreshTokenData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.RefreshTokenData
	for _, t := range f.tokens {
		if t.FamilyID == familyID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeRefreshStore) GetByEntityID(ctx context.Context, entityID string) ([]models.RefreshTokenData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.RefreshTokenData
	for _, t := range f.tokens {
		if t.EntityID == entityID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeRefreshStore) ageReplacement(oldJTI string, issuedAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rep, ok := f.replacements[oldJTI]
	if !ok {
		return
	}
	rep.IssuedAt = issuedAt
	f.replacements[oldJTI] = rep
}

func (f *fakeRefreshStore) isRevokedMarker(jti string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.revoked[jti]
	return ok
}

func newTestRotateService(store *fakeRefreshStore) *RefreshTokenService {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)
	return &RefreshTokenService{
		tokenRepo:    store,
		logger:       logger,
		pollAttempts: 50,
		pollDelay:    time.Millisecond,
	}
}

func seedOldToken(t *testing.T, store *fakeRefreshStore, jti, familyID string) {
	t.Helper()
	err := store.Store(context.Background(), models.RefreshTokenData{
		JTI:        jti,
		EntityID:   "user-1",
		EntityType: "customer",
		Phone:      "+260900000001",
		FamilyID:   familyID,
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(24 * time.Hour),
		Revoked:    false,
	})
	if err != nil {
		t.Fatalf("seed old token: %v", err)
	}
}

func countingMint(access, refresh string) (MintFunc, *int32) {
	var calls int32
	mint := func(familyID string) (*models.TokenPair, string, string, time.Time, error) {
		n := atomic.AddInt32(&calls, 1)
		fid := familyID
		if fid == "" {
			fid = "family-new"
		}
		newJTI := fmt.Sprintf("new-jti-%d", n)
		return &models.TokenPair{
			AccessToken:  access,
			RefreshToken: refresh,
			TokenType:    "Bearer",
			ExpiresIn:    3600,
		}, fid, newJTI, time.Now().Add(24 * time.Hour), nil
	}
	return mint, &calls
}

func TestRotate_ConcurrentDoubleRefresh_SamePair(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestRotateService(store)
	ctx := context.Background()

	oldJTI := "old-jti-concurrent"
	familyID := "family-1"
	seedOldToken(t, store, oldJTI, familyID)

	mint, calls := countingMint("access-shared", "refresh-shared")
	expiresAt := time.Now().Add(24 * time.Hour)

	var wg sync.WaitGroup
	results := make([]*models.TokenPair, 2)
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pair, err := svc.Rotate(ctx, oldJTI, "user-1", "customer", "+260900000001", expiresAt, mint)
			results[idx] = pair
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
		if results[i] == nil {
			t.Fatalf("goroutine %d: nil pair", i)
		}
	}

	if results[0].AccessToken != results[1].AccessToken || results[0].RefreshToken != results[1].RefreshToken {
		t.Fatalf("expected identical token pairs, got %+v vs %+v", results[0], results[1])
	}
	if results[0].AccessToken != "access-shared" || results[0].RefreshToken != "refresh-shared" {
		t.Fatalf("unexpected pair contents: %+v", results[0])
	}
	if *calls != 1 {
		t.Fatalf("expected mint called once, got %d", *calls)
	}
}

func TestRotate_AfterRevoke_ReturnsRevoked(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestRotateService(store)
	ctx := context.Background()

	oldJTI := "old-jti-logout"
	familyID := "family-logout"
	seedOldToken(t, store, oldJTI, familyID)

	if err := svc.Revoke(ctx, oldJTI); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	mint, calls := countingMint("access-x", "refresh-x")
	_, err := svc.Rotate(ctx, oldJTI, "user-1", "customer", "+260900000001", time.Now().Add(time.Hour), mint)
	if !errors.Is(err, ErrRefreshTokenRevoked) {
		t.Fatalf("expected ErrRefreshTokenRevoked, got %v", err)
	}
	if *calls != 0 {
		t.Fatalf("expected mint not called after logout revoke, got %d", *calls)
	}
}

func TestRotate_ReuseOutsideGrace_RevokesFamily(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestRotateService(store)
	ctx := context.Background()

	oldJTI := "old-jti-theft"
	familyID := "family-theft"
	seedOldToken(t, store, oldJTI, familyID)

	mint, _ := countingMint("access-winner", "refresh-winner")
	expiresAt := time.Now().Add(24 * time.Hour)

	pair, err := svc.Rotate(ctx, oldJTI, "user-1", "customer", "+260900000001", expiresAt, mint)
	if err != nil {
		t.Fatalf("winner Rotate: %v", err)
	}
	if pair == nil {
		t.Fatal("winner Rotate: nil pair")
	}

	rep, err := store.GetReplacement(ctx, oldJTI)
	if err != nil || rep == nil {
		t.Fatalf("expected replacement after winner rotate, err=%v", err)
	}
	newJTI := rep.NewJTI

	// Age replacement beyond grace → second use is theft.
	store.ageReplacement(oldJTI, time.Now().Add(-(refreshReuseGrace + time.Second)))

	mint2, calls2 := countingMint("access-thief", "refresh-thief")
	_, err = svc.Rotate(ctx, oldJTI, "user-1", "customer", "+260900000001", expiresAt, mint2)
	if !errors.Is(err, ErrRefreshTokenRevoked) {
		t.Fatalf("expected ErrRefreshTokenRevoked outside grace, got %v", err)
	}
	if *calls2 != 0 {
		t.Fatalf("expected mint not called on theft path, got %d", *calls2)
	}

	// Family tokens (including the winner's new refresh) must be hard-revoked.
	if !store.isRevokedMarker(newJTI) {
		t.Fatalf("expected family token %s to be revoked after reuse outside grace", newJTI)
	}
}
