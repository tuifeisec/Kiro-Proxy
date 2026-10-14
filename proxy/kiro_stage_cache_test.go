package proxy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var errKiroStageMockProtection = errors.New("mock protection failed")
var errKiroStageMockUnprotection = errors.New("mock unprotection failed")

func newTestKiroStageVerificationCache(root, validator string) *kiroStageVerificationCache {
	key := []byte("kiro-stage-verification-cache-test-key")
	protect := func(plain []byte) ([]byte, error) {
		tag := hmac.New(sha256.New, key)
		_, _ = tag.Write(plain)
		return append(tag.Sum(nil), plain...), nil
	}
	unprotect := func(protected []byte) ([]byte, error) {
		if len(protected) < sha256.Size {
			return nil, errors.New("mock protected content is truncated")
		}
		mac, plain := protected[:sha256.Size], protected[sha256.Size:]
		tag := hmac.New(sha256.New, key)
		_, _ = tag.Write(plain)
		if !hmac.Equal(mac, tag.Sum(nil)) {
			return nil, errors.New("mock protected content integrity check failed")
		}
		return append([]byte(nil), plain...), nil
	}
	return newKiroStageVerificationCache(root, validator, protect, unprotect)
}

func testKiroStageCacheHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestKiroStageVerificationCachePersistsAcrossInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	target := filepath.Join(t.TempDir(), "bundle.js")
	installedHash := testKiroStageCacheHash("installed")
	originalHash := testKiroStageCacheHash("original")
	adapterHash := testKiroStageCacheHash("adapter")

	first := newTestKiroStageVerificationCache(root, testKiroStageCacheHash("validator"))
	if got, hit, err := first.Lookup(target, installedHash, adapterHash); err != nil || hit || got != "" {
		t.Fatalf("missing lookup = (%q, %v, %v), want (empty, false, nil)", got, hit, err)
	}
	if err := first.Store(target, installedHash, originalHash, adapterHash); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	second := newTestKiroStageVerificationCache(root, first.validator)
	if got, hit, err := second.Lookup(target, installedHash, adapterHash); err != nil || !hit || got != originalHash {
		t.Fatalf("persistent lookup = (%q, %v, %v), want (%q, true, nil)", got, hit, err, originalHash)
	}
}

func TestKiroStageVerificationCacheInvalidatesEveryKeyField(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	target := filepath.Join(t.TempDir(), "bundle.js")
	installedHash := testKiroStageCacheHash("installed")
	originalHash := testKiroStageCacheHash("original")
	adapterHash := testKiroStageCacheHash("adapter")
	validator := testKiroStageCacheHash("validator")
	cache := newTestKiroStageVerificationCache(root, validator)
	if err := cache.Store(target, installedHash, originalHash, adapterHash); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	otherPath := filepath.Join(filepath.Dir(target), "other-bundle.js")
	otherInstalledHash := testKiroStageCacheHash("other-installed")
	otherAdapterHash := testKiroStageCacheHash("other-adapter")
	for name, lookup := range map[string]func() (string, bool, error){
		"target": func() (string, bool, error) {
			return cache.Lookup(otherPath, installedHash, adapterHash)
		},
		"installed hash": func() (string, bool, error) {
			return cache.Lookup(target, otherInstalledHash, adapterHash)
		},
		"adapter hash": func() (string, bool, error) {
			return cache.Lookup(target, installedHash, otherAdapterHash)
		},
		"validator": func() (string, bool, error) {
			return newTestKiroStageVerificationCache(root, testKiroStageCacheHash("new-validator")).Lookup(target, installedHash, adapterHash)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got, hit, err := lookup(); err != nil || hit || got != "" {
				t.Fatalf("changed-key lookup = (%q, %v, %v), want (empty, false, nil)", got, hit, err)
			}
		})
	}
}

func TestKiroStageVerificationCacheNormalizesTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	target := filepath.Join(t.TempDir(), "Bundle.js")
	alias := filepath.Join(filepath.Dir(target), "sub", "..", filepath.Base(target))
	if runtime.GOOS == "windows" {
		alias = strings.ToUpper(alias)
	}
	cache := newTestKiroStageVerificationCache(root, testKiroStageCacheHash("validator"))
	installedHash := testKiroStageCacheHash("installed")
	originalHash := testKiroStageCacheHash("original")
	adapterHash := testKiroStageCacheHash("adapter")
	if err := cache.Store(target, installedHash, originalHash, adapterHash); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if got, hit, err := cache.Lookup(alias, installedHash, adapterHash); err != nil || !hit || got != originalHash {
		t.Fatalf("normalized lookup = (%q, %v, %v), want (%q, true, nil)", got, hit, err, originalHash)
	}
}

func TestKiroStageVerificationCacheRejectsCorruptionAndForgedBinding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	target := filepath.Join(t.TempDir(), "bundle.js")
	installedHash := testKiroStageCacheHash("installed")
	originalHash := testKiroStageCacheHash("original")
	adapterHash := testKiroStageCacheHash("adapter")
	cache := newTestKiroStageVerificationCache(root, testKiroStageCacheHash("validator"))
	identity, key, err := cache.identity(target, installedHash, adapterHash)
	if err != nil {
		t.Fatal(err)
	}
	receipt := kiroStageVerificationReceipt{
		Schema:        identity.Schema,
		Target:        identity.Target + ".forged",
		InstalledHash: identity.InstalledHash,
		OriginalHash:  originalHash,
		AdapterHash:   identity.AdapterHash,
		Validator:     identity.Validator,
	}
	plain, err := encodeKiroStageVerificationReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cache.protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	path := cache.receiptPath(key)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Lookup(target, installedHash, adapterHash); err == nil || hit {
		t.Fatalf("forged receipt lookup = (hit %v, error %v), want an error and miss", hit, err)
	}
	if _, hit, err := newTestKiroStageVerificationCache(root, cache.validator).Lookup(target, installedHash, adapterHash); err == nil || hit {
		t.Fatalf("forged receipt was published to memory: hit=%v err=%v", hit, err)
	}

	receipt.Target = identity.Target
	plain, err = encodeKiroStageVerificationReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var envelope kiroStageVerificationEnvelope
	if err := json.Unmarshal(plain, &envelope); err != nil {
		t.Fatal(err)
	}
	changedOriginalHash := testKiroStageCacheHash("changed-original")
	envelope.Payload = bytes.Replace(envelope.Payload, []byte(originalHash), []byte(changedOriginalHash), 1)
	plain, err = json.Marshal(envelope) // 保留旧摘要，模拟 DPAPI 未报错但返回内容已损坏。
	if err != nil {
		t.Fatal(err)
	}
	sealed, err = cache.protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := newTestKiroStageVerificationCache(root, cache.validator).Lookup(target, installedHash, adapterHash); err == nil || hit {
		t.Fatalf("payload digest mismatch lookup = (hit %v, error %v), want an error and miss", hit, err)
	}

	sealed[len(sealed)-1] ^= 0xff
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := newTestKiroStageVerificationCache(root, cache.validator).Lookup(target, installedHash, adapterHash); err == nil || hit {
		t.Fatalf("corrupt receipt lookup = (hit %v, error %v), want an error and miss", hit, err)
	}
}

func TestKiroStageVerificationCacheRejectsInvalidKeyHashes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	target := filepath.Join(t.TempDir(), "bundle.js")
	installedHash := testKiroStageCacheHash("installed")
	adapterHash := testKiroStageCacheHash("adapter")
	validator := testKiroStageCacheHash("validator")
	cases := map[string]func() error{
		"installed hash": func() error {
			_, _, err := newTestKiroStageVerificationCache(root, validator).Lookup(target, "invalid", adapterHash)
			return err
		},
		"adapter hash": func() error {
			_, _, err := newTestKiroStageVerificationCache(root, validator).Lookup(target, installedHash, "invalid")
			return err
		},
		"validator hash": func() error {
			_, _, err := newTestKiroStageVerificationCache(root, "invalid").Lookup(target, installedHash, adapterHash)
			return err
		},
		"original hash": func() error {
			return newTestKiroStageVerificationCache(root, validator).Store(target, installedHash, "invalid", adapterHash)
		},
	}
	for name, check := range cases {
		t.Run(name, func(t *testing.T) {
			if err := check(); err == nil {
				t.Fatal("operation with invalid SHA256 unexpectedly succeeded")
			}
		})
	}
}

func TestKiroStageVerificationCacheReturnsReadAndDecryptErrors(t *testing.T) {
	target := filepath.Join(t.TempDir(), "bundle.js")
	installedHash := testKiroStageCacheHash("installed")
	adapterHash := testKiroStageCacheHash("adapter")
	cache := newTestKiroStageVerificationCache(filepath.Join(t.TempDir(), "cache"), testKiroStageCacheHash("validator"))
	_, key, err := cache.identity(target, installedHash, adapterHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cache.receiptPath(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Lookup(target, installedHash, adapterHash); err == nil || hit {
		t.Fatalf("directory read lookup = (hit %v, error %v), want an error and miss", hit, err)
	} else {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("directory read error = %T %v, want the underlying path error", err, err)
		}
	}

	decryptFailure := newTestKiroStageVerificationCache(filepath.Join(t.TempDir(), "cache"), cache.validator)
	decryptFailure.unprotect = func([]byte) ([]byte, error) { return nil, errKiroStageMockUnprotection }
	if err := os.MkdirAll(decryptFailure.root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, key, err = decryptFailure.identity(target, installedHash, adapterHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decryptFailure.receiptPath(key), []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := decryptFailure.Lookup(target, installedHash, adapterHash); !errors.Is(err, errKiroStageMockUnprotection) || hit {
		t.Fatalf("decrypt failure lookup = (hit %v, error %v), want wrapped mock error and miss", hit, err)
	}
}

func TestKiroStageVerificationCacheFailedStoreDoesNotCache(t *testing.T) {
	target := filepath.Join(t.TempDir(), "bundle.js")
	installedHash := testKiroStageCacheHash("installed")
	originalHash := testKiroStageCacheHash("original")
	adapterHash := testKiroStageCacheHash("adapter")
	root := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := newTestKiroStageVerificationCache(root, testKiroStageCacheHash("validator"))
	if err := cache.Store(target, installedHash, originalHash, adapterHash); err == nil {
		t.Fatal("Store() unexpectedly succeeded with a file as cache directory")
	} else {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("write failure = %T %v, want the underlying path error", err, err)
		}
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, hit, err := cache.Lookup(target, installedHash, adapterHash); err != nil || hit || got != "" {
		t.Fatalf("lookup after failed Store = (%q, %v, %v), want (empty, false, nil)", got, hit, err)
	}

	protectFailure := newTestKiroStageVerificationCache(filepath.Join(t.TempDir(), "cache"), cache.validator)
	protectFailure.protect = func([]byte) ([]byte, error) { return nil, errKiroStageMockProtection }
	if err := protectFailure.Store(target, installedHash, originalHash, adapterHash); !errors.Is(err, errKiroStageMockProtection) {
		t.Fatalf("Store() error = %v, want wrapped mock protection error", err)
	}
	protectFailure.protect = cache.protect
	if got, hit, err := protectFailure.Lookup(target, installedHash, adapterHash); err != nil || hit || got != "" {
		t.Fatalf("lookup after protection failure = (%q, %v, %v), want (empty, false, nil)", got, hit, err)
	}
}

func TestKiroStageVerificationCacheConcurrentSameStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	target := filepath.Join(t.TempDir(), "bundle.js")
	installedHash := testKiroStageCacheHash("installed")
	originalHash := testKiroStageCacheHash("original")
	adapterHash := testKiroStageCacheHash("adapter")
	validator := testKiroStageCacheHash("validator")
	const writers = 20
	start := make(chan struct{})
	errCh := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache := newTestKiroStageVerificationCache(root, validator)
			<-start
			errCh <- cache.Store(target, installedHash, originalHash, adapterHash)
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent Store() error = %v", err)
		}
	}
	cache := newTestKiroStageVerificationCache(root, validator)
	if got, hit, err := cache.Lookup(target, installedHash, adapterHash); err != nil || !hit || got != originalHash {
		t.Fatalf("lookup after concurrent Store = (%q, %v, %v), want (%q, true, nil)", got, hit, err, originalHash)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	receiptCount := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".receipt") {
			receiptCount++
		}
	}
	if receiptCount != 1 {
		t.Fatalf("persisted receipt count = %d, want 1", receiptCount)
	}
}
