package codex

import (
	"context"
	"errors"
	"fmt"
	"kiro-proxy/store"
	"path/filepath"
	"testing"
	"time"
)

func rotationModels(t *testing.T) (*store.DB, store.Model, store.Model) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rotation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a, b := expiredCodexModel("account", "old-refresh"), expiredCodexModel("account", "old-refresh")
	a.ID, b.ID = "a", "b"
	for _, model := range []*store.Model{&a, &b} {
		if err := db.CreateModel(model); err != nil {
			t.Fatal(err)
		}
	}
	return db, a, b
}

func TestCredentialRotationPropagatesMultipleGenerations(t *testing.T) {
	db, a, b := rotationModels(t)
	var consumed []string
	manager := NewCredentialManagerWithRefresher(db, func(_ context.Context, refresh, _ string) (*TokenResponse, error) {
		consumed = append(consumed, refresh)
		n := len(consumed)
		return &TokenResponse{AccessToken: fmt.Sprintf("access-%d", n), RefreshToken: fmt.Sprintf("refresh-%d", n), ExpiresIn: 3600}, nil
	})
	if _, err := manager.Ensure(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	// b 未曾请求过，但强刷必须使用第一轮传播后的 token。
	if _, err := manager.ForceRefresh(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	got, err := manager.Ensure(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumed) != 2 || consumed[0] != "old-refresh" || consumed[1] != "refresh-1" || got.RefreshToken != "refresh-2" {
		t.Fatalf("rotation chain broken: consumed=%v refresh=%q", consumed, got.RefreshToken)
	}
	for _, model := range []store.Model{a, b} {
		saved, err := db.GetModel(model.ID)
		if err != nil || saved.RefreshToken != "refresh-2" || saved.AccessTokenExpiresAt != got.AccessTokenExpiresAt {
			t.Fatalf("peer propagation failed: %v", err)
		}
	}
}

func TestCredentialRotationLateOwnershipRechecksGeneration(t *testing.T) {
	db, a, b := rotationModels(t)
	calls := 0
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		calls++
		return refreshedToken("account"), nil
	})
	staleKey := credentialKey(b) // B 已读取旧代，但尚未取得 ownership。
	if _, err := manager.Ensure(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	call, leader := manager.beginRefresh(staleKey)
	if !leader {
		t.Fatal("previous flight was not removed")
	}
	manager.runRefresh(context.Background(), b, staleKey, true, call)
	manager.endRefresh(staleKey, call)
	if !call.retry || call.err != nil || calls != 1 {
		t.Fatalf("late owner consumed old token: calls=%d retry=%v err=%v", calls, call.retry, call.err)
	}
}

func TestCredentialPendingRecoveryKeepsAbsoluteExpiryAndAllPeers(t *testing.T) {
	db, a, b := rotationModels(t)
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		t.Fatal("pending recovery must not refresh again")
		return nil, nil
	})
	rotated := a
	rotated.APIKey, rotated.RefreshToken = "issued-access", "issued-refresh"
	// 模拟 token 在 pending 中已经停留一段时间，时间戳必须原样保留。
	rotated.AccessTokenExpiresAt = time.Now().Add(11 * time.Minute).Unix()
	manager.savePending(pendingCredential{key: credentialKey(a), original: a, updated: rotated})
	got, err := manager.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []store.Model{a, b} {
		saved, err := db.GetModel(model.ID)
		if err != nil || saved.AccessTokenExpiresAt != rotated.AccessTokenExpiresAt || saved.RefreshToken != rotated.RefreshToken {
			t.Fatalf("pending propagation/expiry changed: %v", err)
		}
	}
	if got.AccessTokenExpiresAt != rotated.AccessTokenExpiresAt || len(manager.pending) != 0 {
		t.Fatal("pending expiry was recalculated or recovery retained")
	}
}

func TestCredentialAuthJSONPreservesRawCASValues(t *testing.T) {
	db, a, _ := rotationModels(t)
	a.APIKey, a.CodexAccountID = "", ""
	a.RefreshToken = `{"access_token":"old-access","refresh_token":"payload-refresh","expires_at":1}`
	if err := db.UpdateModel(&a); err != nil {
		t.Fatal(err)
	}
	manager := NewCredentialManagerWithRefresher(db, func(_ context.Context, refresh, _ string) (*TokenResponse, error) {
		if refresh != "payload-refresh" {
			t.Fatalf("raw payload sent as refresh token")
		}
		return refreshedToken("payload"), nil
	})
	got, err := manager.Ensure(context.Background(), a)
	if err != nil || got.RefreshToken != "new-refresh-payload" {
		t.Fatalf("auth JSON CAS failed: %v", err)
	}
}

func TestCredentialRotationRejectsChangedRouteAfterRefresh(t *testing.T) {
	db, a, _ := rotationModels(t)
	manager := NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*TokenResponse, error) {
		changed := a
		changed.BaseURL = "https://changed.invalid/backend-api"
		if err := db.UpdateModel(&changed); err != nil {
			t.Fatal(err)
		}
		return refreshedToken("account"), nil
	})
	_, err := manager.Ensure(context.Background(), a)
	if !errors.Is(err, store.ErrCodexCredentialConflict) {
		t.Fatalf("route change allowed: %v", err)
	}
}

func TestCredentialRotationSeparatesSameAccountGenerations(t *testing.T) {
	a := expiredCodexModel("account", "refresh-a")
	b := a
	b.RefreshToken = "refresh-b"
	started, release := make(chan string, 2), make(chan struct{})
	manager := NewCredentialManagerWithRefresher(nil, func(_ context.Context, refresh, _ string) (*TokenResponse, error) {
		started <- refresh
		<-release
		return &TokenResponse{AccessToken: "access-" + refresh, RefreshToken: "next-" + refresh, ExpiresIn: 3600}, nil
	})
	results := make(chan store.Model, 2)
	for _, model := range []store.Model{a, b} {
		go func(model store.Model) {
			result, _ := manager.ForceRefresh(context.Background(), model)
			results <- result
		}(model)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("independent generations were merged")
		}
	}
	close(release)
	x, y := <-results, <-results
	if x.RefreshToken == y.RefreshToken {
		t.Fatal("independent generation result was shared")
	}
}
