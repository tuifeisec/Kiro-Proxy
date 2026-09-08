package codex

import (
	"context"
	"errors"
	"fmt"
	"kiro-proxy/store"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func expiredCodexModel(account, refresh string) store.Model {
	return store.Model{
		ID:                   "model-" + account,
		Name:                 "test-" + account,
		ModelID:              "gpt-test",
		Provider:             "codex",
		BaseURL:              DefaultBaseURL,
		Endpoint:             DefaultEndpoint,
		APIKey:               "old-access-" + account,
		RefreshToken:         refresh,
		CodexAccountID:       account,
		AccessTokenExpiresAt: time.Now().Add(-time.Hour).Unix(),
		Enabled:              true,
	}
}

func refreshedToken(account string) *TokenResponse {
	return &TokenResponse{
		AccessToken:  "new-access-" + account,
		RefreshToken: "new-refresh-" + account,
		ExpiresIn:    int64((time.Hour).Seconds()),
	}
}

func TestCredentialManagerSameAccountSingleflight(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	manager := NewCredentialManagerWithRefresher(nil, func(ctx context.Context, refresh, _ string) (*TokenResponse, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return refreshedToken("acct"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	model := expiredCodexModel("acct", "refresh-acct")
	results := make(chan error, 2)
	go func() {
		_, err := manager.ForceRefresh(context.Background(), model)
		results <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	go func() {
		_, err := manager.Ensure(context.Background(), model)
		results <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("Ensure failed: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestCredentialManagerDifferentAccountsRunInParallel(t *testing.T) {
	var calls atomic.Int32
	started := make(chan string, 2)
	release := make(chan struct{})
	manager := NewCredentialManagerWithRefresher(nil, func(ctx context.Context, refresh, _ string) (*TokenResponse, error) {
		calls.Add(1)
		started <- refresh
		select {
		case <-release:
			return refreshedToken(refresh), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	results := make(chan error, 2)
	for _, account := range []string{"a", "b"} {
		model := expiredCodexModel(account, "refresh-"+account)
		go func() {
			_, err := manager.Ensure(context.Background(), model)
			results <- err
		}()
	}
	seen := map[string]bool{}
	deadline := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case refresh := <-started:
			seen[refresh] = true
		case <-deadline:
			t.Fatalf("only %d account refreshes started", len(seen))
		}
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("Ensure failed: %v", err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("refresh calls = %d, want 2", got)
	}
}

func TestCredentialManagerWaiterCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	manager := NewCredentialManagerWithRefresher(nil, func(ctx context.Context, _, _ string) (*TokenResponse, error) {
		close(started)
		select {
		case <-release:
			return refreshedToken("acct"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	model := expiredCodexModel("acct", "refresh-acct")
	leaderResult := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(context.Background(), model)
		leaderResult <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.Ensure(ctx, model); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter error = %v, want context deadline", err)
	}
	close(release)
	if err := <-leaderResult; err != nil {
		t.Fatalf("leader failed: %v", err)
	}
}

func TestCredentialManagerRetriesAfterLeaderCancellation(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	manager := NewCredentialManagerWithRefresher(nil, func(ctx context.Context, _, _ string) (*TokenResponse, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return refreshedToken("acct"), nil
	})
	model := expiredCodexModel("acct", "refresh-acct")
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(leaderCtx, model)
		leaderResult <- err
	}()
	<-started
	followerResult := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(context.Background(), model)
		followerResult <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancelLeader()
	if err := <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want cancellation", err)
	}
	if err := <-followerResult; err != nil {
		t.Fatalf("follower failed to retry: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls.Load())
	}
}

func TestCredentialManagerRefreshErrorIsReturned(t *testing.T) {
	want := errors.New("refresh failed")
	manager := NewCredentialManagerWithRefresher(nil, func(context.Context, string, string) (*TokenResponse, error) {
		return nil, want
	})
	_, err := manager.Ensure(context.Background(), expiredCodexModel("acct", "refresh-acct"))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestCredentialManagerRotationCASAndPreservesConfig(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := expiredCodexModel("acct", "refresh-acct")
	model.Note = "keep-me"
	model.CodexUsageJSON = `{"before":true}`
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		calls.Add(1)
		return refreshedToken("acct"), nil
	})
	// The route copy is deliberately stale.  Ensure must use the DB row.
	routeCopy := model
	routeCopy.APIKey = "stale-route-access"
	got, err := manager.Ensure(context.Background(), routeCopy)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "new-access-acct" || got.RefreshToken != "new-refresh-acct" {
		t.Fatalf("unexpected refreshed credentials: %#v", got)
	}
	if got.Note != "keep-me" || got.CodexUsageJSON != `{"before":true}` {
		t.Fatalf("credential CAS changed config/usage: note=%q usage=%q", got.Note, got.CodexUsageJSON)
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != got.APIKey || saved.RefreshToken != got.RefreshToken {
		t.Fatalf("DB credentials not rotated: %#v", saved)
	}
	if saved.Note != "keep-me" || saved.CodexUsageJSON != `{"before":true}` {
		t.Fatalf("DB config/usage overwritten: note=%q usage=%q", saved.Note, saved.CodexUsageJSON)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestCredentialManagerPropagatesAccountRotationAfterFlight(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := expiredCodexModel("acct", "refresh-acct")
	first.ID = "model-first"
	second := expiredCodexModel("acct", "refresh-acct")
	second.ID = "model-second"
	if err := db.CreateModel(&first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateModel(&second); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		calls.Add(1)
		return refreshedToken("acct"), nil
	})
	if _, err := manager.Ensure(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// The second model starts after the first flight ended.  The same-generation
	// transaction already propagated the rotation to its row.
	got, err := manager.Ensure(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "new-access-acct" || got.RefreshToken != "new-refresh-acct" {
		t.Fatalf("account token not propagated: %#v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestCredentialManagerUsesCurrentValidCredentialAfterCASConflict(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := expiredCodexModel("acct", "refresh-acct")
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	current := model
	current.APIKey = "manual-current"
	current.RefreshToken = "manual-refresh"
	current.AccessTokenExpiresAt = time.Now().Add(time.Hour).Unix()
	if err := db.UpdateModel(&current); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		calls.Add(1)
		return refreshedToken("acct"), nil
	})
	got, err := manager.Ensure(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "manual-current" || got.RefreshToken != "manual-refresh" {
		t.Fatalf("stale credential reused: %#v", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("refresh calls = %d, want 0", calls.Load())
	}
}

func TestCredentialManagerKeepsRotatedTokenWhenPersistenceFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	model := expiredCodexModel("acct", "refresh-acct")
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		// Force the CAS write to fail after the provider has rotated the token.
		_ = db.Close()
		return refreshedToken("acct"), nil
	})
	_, err = manager.Ensure(context.Background(), model)
	if err == nil {
		t.Fatal("Ensure succeeded despite persistence failure")
	}
	key := credentialKey(model)
	manager.mu.Lock()
	_, pending := manager.pending[pendingKey(model.ID, key)]
	manager.mu.Unlock()
	if !pending {
		t.Fatal("rotated token was not retained for persistence retry")
	}
}

func TestCredentialManagerSharesPendingRotationAcrossAccountModels(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := expiredCodexModel("acct", "refresh-acct")
	first.ID = "model-first"
	second := expiredCodexModel("acct", "refresh-acct")
	second.ID = "model-second"
	if err := db.CreateModel(&first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateModel(&second); err != nil {
		t.Fatal(err)
	}
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		t.Fatal("pending rotation should avoid a second provider refresh")
		return nil, nil
	})
	rotated := first
	applyTokenResponse(&rotated, refreshedToken("acct"))
	manager.pending[pendingKey(first.ID, credentialKey(first))] = pendingCredential{
		key:      credentialKey(first),
		original: first,
		updated:  rotated,
	}
	got, err := manager.Ensure(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "new-access-acct" || got.RefreshToken != "new-refresh-acct" {
		t.Fatalf("pending token not persisted to second model: %#v", got)
	}
	saved, err := db.GetModel(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != "new-access-acct" || saved.RefreshToken != "new-refresh-acct" {
		t.Fatalf("second model was not rotated: %#v", saved)
	}
}

func TestCredentialManagerPendingRotationDoesNotOverwriteManualImport(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := expiredCodexModel("acct", "refresh-acct")
	first.ID = "model-first"
	second := expiredCodexModel("acct", "refresh-acct")
	second.ID = "model-second"
	if err := db.CreateModel(&first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateModel(&second); err != nil {
		t.Fatal(err)
	}
	manual := second
	manual.RefreshToken = "manual-refresh"
	manual.APIKey = "manual-access"
	manual.AccessTokenExpiresAt = 1
	if err := db.UpdateCodexTokenAndUsage(&manual); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		calls.Add(1)
		return nil, errors.New("manual credential must be refreshed independently")
	})
	rotated := first
	rotatedToken := refreshedToken("acct")
	applyTokenResponse(&rotated, rotatedToken)
	manager.pending[pendingKey(first.ID, credentialKey(first))] = pendingCredential{
		key: credentialKey(first), original: first, updated: rotated,
	}
	_, err = manager.Ensure(context.Background(), second)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("manual credential unexpectedly reused pending rotation: err=%v calls=%d", err, calls.Load())
	}
	saved, err := db.GetModel(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != "manual-access" || saved.RefreshToken != "manual-refresh" {
		t.Fatalf("manual credential was overwritten: %#v", saved)
	}
}

func TestCredentialManagerDoesNotMutateInput(t *testing.T) {
	manager := NewCredentialManagerWithRefresher(nil, func(context.Context, string, string) (*TokenResponse, error) {
		return refreshedToken("acct"), nil
	})
	model := expiredCodexModel("acct", "refresh-acct")
	before := model
	got, err := manager.Ensure(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey == before.APIKey || model.APIKey != before.APIKey || model.RefreshToken != before.RefreshToken {
		t.Fatalf("route/input model was mutated: input=%#v result=%#v", model, got)
	}
}

func TestCredentialKeyNeverUsesAccessToken(t *testing.T) {
	a := expiredCodexModel("", "same-refresh")
	b := a
	b.APIKey = "different-access"
	if gotA, gotB := credentialKey(a), credentialKey(b); gotA != gotB {
		t.Fatalf("missing identity key changed with access token: %q vs %q", gotA, gotB)
	}
}

func TestCredentialManagerCASMethodConflict(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := expiredCodexModel("acct", "refresh-acct")
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	updated := model
	updated.APIKey = "new-access"
	updated.RefreshToken = "new-refresh"
	updated.AccessTokenExpiresAt = time.Now().Add(time.Hour).Unix()
	other := model
	other.APIKey = "other-access"
	other.RefreshToken = "other-refresh"
	other.AccessTokenExpiresAt = time.Now().Add(time.Hour).Unix()
	if err := db.UpdateCodexTokenAndUsage(&other); err != nil {
		t.Fatal(err)
	}
	_, err = db.UpdateCodexTokenCAS(&model, &updated)
	if !errors.Is(err, store.ErrCodexCredentialConflict) {
		t.Fatalf("CAS error = %v, want conflict", err)
	}
}

func TestCredentialManagerInputErrors(t *testing.T) {
	manager := NewCredentialManager(nil)
	if _, err := manager.Ensure(context.Background(), store.Model{}); err != nil {
		t.Fatal(fmt.Errorf("non-Codex model should pass through: %w", err))
	}
}
