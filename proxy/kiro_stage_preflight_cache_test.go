package proxy

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStagePreflightTestCache(t *testing.T, root, validator string) *kiroStageVerificationCache {
	t.Helper()
	protect := func(data []byte) ([]byte, error) { return append([]byte("mock-protected:"), data...), nil }
	unprotect := func(data []byte) ([]byte, error) {
		if !bytes.HasPrefix(data, []byte("mock-protected:")) {
			return nil, errors.New("mock receipt integrity failure")
		}
		return append([]byte(nil), data[len("mock-protected:"):]...), nil
	}
	return newKiroStageVerificationCache(root, validator, protect, unprotect)
}

func writeStagePreflightFixture(t *testing.T, source []byte) (string, string) {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "Kiro.exe")
	target := kiroStageBundlePath(executable)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, source, 0600); err != nil {
		t.Fatal(err)
	}
	return executable, target
}

func stagePreflightMockCandidate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	original := []byte("const sentinel=1;\n")
	payload := []byte("const sentinel=2;\n")
	candidate, err := wrapKiroStageStructuralCandidate(original, payload, "mock-family", kiroStageStructuralAdapterHash(), []kiroStageStructuralEdit{{Offset: len("const sentinel="), Original: "1", Installed: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	return original, candidate
}

func TestKiroStagePreflightCachesOnlyVerifiedInstalledBytes(t *testing.T) {
	original, candidate := stagePreflightMockCandidate(t)
	executable, target := writeStagePreflightFixture(t, original)
	root := t.TempDir()
	validator := kiroStageSHA256([]byte("test-validator"))
	cache := newStagePreflightTestCache(t, root, validator)
	calls := 0
	compile := func(source []byte) ([]byte, error) {
		calls++
		if !bytes.Equal(source, original) {
			return nil, errors.New("mock compiler: unexpected source")
		}
		return append([]byte(nil), candidate...), nil
	}
	plan, err := prepareKiroStageInstallWith(executable, cache, compile)
	if err != nil || calls != 1 || plan.cacheHit {
		t.Fatalf("first preflight: calls=%d plan=%+v err=%v", calls, plan, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("preflight wrote a persistent receipt: %v, %v", entries, err)
	}
	if _, hit, err := cache.Lookup(target, kiroStageSHA256(candidate), kiroStageStructuralAdapterHash()); err != nil || hit {
		t.Fatalf("preflight published uninstalled bytes: hit=%t err=%v", hit, err)
	}
	if _, err := installKiroStageBridge(plan); err != nil {
		t.Fatal(err)
	}
	for _, instance := range []*kiroStageVerificationCache{cache, newStagePreflightTestCache(t, root, validator)} {
		cached, err := prepareKiroStageInstallWith(executable, instance, compile)
		if err != nil || !cached.cacheHit || len(cached.candidate) != 0 || calls != 1 {
			t.Fatalf("cache did not skip compiler: calls=%d plan=%+v err=%v", calls, cached, err)
		}
		if result, err := installKiroStageBridge(cached); err != nil || result.backupPath != "" {
			t.Fatalf("cache hit rewrote installation: result=%+v err=%v", result, err)
		}
	}
	// 相同长度和 mtime 不足以命中；完整文件摘要必须变化。
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), candidate...)
	changed[0] ^= 1
	if err := os.WriteFile(target, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareKiroStageInstallWith(executable, cache, compile); err == nil || !strings.Contains(err.Error(), "代码摘要不符") {
		t.Fatalf("changed bytes with unchanged metadata accepted: %v", err)
	}
}

func TestKiroStagePreflightDoesNotTrustSelfConsistentRecord(t *testing.T) {
	_, candidate := stagePreflightMockCandidate(t)
	executable, target := writeStagePreflightFixture(t, candidate)
	cache := newStagePreflightTestCache(t, t.TempDir(), kiroStageSHA256([]byte("validator")))
	want := errors.New("independent validator rejected forged structure")
	calls := 0
	_, err := prepareKiroStageInstallWith(executable, cache, func([]byte) ([]byte, error) {
		calls++
		return nil, want
	})
	if calls != 1 || !errors.Is(err, want) {
		t.Fatalf("self-consistent record bypassed validator: calls=%d err=%v", calls, err)
	}
	if _, hit, err := cache.Lookup(target, kiroStageSHA256(candidate), kiroStageStructuralAdapterHash()); err != nil || hit {
		t.Fatalf("failed validation cached: hit=%t err=%v", hit, err)
	}
}

func TestKiroStagePreflightRaceDoesNotPublishReceipt(t *testing.T) {
	original, candidate := stagePreflightMockCandidate(t)
	executable, target := writeStagePreflightFixture(t, original)
	cache := newStagePreflightTestCache(t, t.TempDir(), kiroStageSHA256([]byte("validator")))
	plan, err := prepareKiroStageInstallWith(executable, cache, func([]byte) ([]byte, error) { return candidate, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("changed after preflight"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installKiroStageBridge(plan); err == nil || !strings.Contains(err.Error(), "预检后发生变化") {
		t.Fatalf("race accepted: %v", err)
	}
	if _, hit, err := cache.Lookup(target, kiroStageSHA256(candidate), kiroStageStructuralAdapterHash()); err != nil || hit {
		t.Fatalf("failed install cached: hit=%t err=%v", hit, err)
	}
}

func TestKiroStagePreflightCacheSaveFailureReportsInstalledState(t *testing.T) {
	original, candidate := stagePreflightMockCandidate(t)
	executable, target := writeStagePreflightFixture(t, original)
	want := errors.New("mock protect receipt denied")
	cache := newKiroStageVerificationCache(t.TempDir(), kiroStageSHA256([]byte("validator")), func([]byte) ([]byte, error) {
		return nil, want
	}, func(data []byte) ([]byte, error) { return data, nil })
	plan, err := prepareKiroStageInstallWith(executable, cache, func([]byte) ([]byte, error) { return candidate, nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := installKiroStageBridge(plan)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "已核验或安装") || result.backupPath == "" {
		t.Fatalf("save failure hid installed state: result=%+v err=%v", result, err)
	}
	installed, readErr := os.ReadFile(target)
	backup, backupErr := os.ReadFile(result.backupPath)
	if readErr != nil || backupErr != nil || !bytes.Equal(installed, candidate) || !bytes.Equal(backup, original) {
		t.Fatalf("verified installation or backup changed after receipt failure: %v %v", readErr, backupErr)
	}
	if _, hit, err := cache.Lookup(target, kiroStageSHA256(candidate), kiroStageStructuralAdapterHash()); err != nil || hit {
		t.Fatalf("failed receipt save published memory: hit=%t err=%v", hit, err)
	}
}

// 只读真实输入；写入和完整安装都限定在测试临时目录。
func TestKiroStagePreflightRealPerformance(t *testing.T) {
	path := os.Getenv("KIRO_STAGE_PERFORMANCE_BUNDLE")
	if path == "" {
		t.Skip("KIRO_STAGE_PERFORMANCE_BUNDLE 未设置")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, target := writeStagePreflightFixture(t, original)
	root := t.TempDir()
	validator := kiroStageSHA256([]byte("performance-validator"))
	protect, unprotect, err := platformKiroStageCacheProtection()
	if err != nil {
		t.Fatal(err)
	}
	cache := newKiroStageVerificationCache(root, validator, protect, unprotect)
	calls := 0
	compile := func(source []byte) ([]byte, error) {
		calls++
		return buildKiroStageStructuralCandidate(source)
	}
	start := time.Now()
	plan, err := prepareKiroStageInstallWith(executable, cache, compile)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first_preflight bytes=%d elapsed=%s compiler_calls=%d candidate_sha256=%s", len(original), time.Since(start), calls, plan.candidateHash)
	if kiroStageSHA256(original) == "da956c67134e9de239795ac6bbd3c05f9312516d1685b45e217bc8d2826b9bb7" && plan.candidateHash != "05893583b44233eaaa1eaaafd82b39bbc1f6c0d07f2e0c90c0293e0942fee8fe" {
		t.Fatalf("restored compiler differs from the installed final candidate: %s", plan.candidateHash)
	}
	start = time.Now()
	if _, err := installKiroStageBridge(plan); err != nil {
		t.Fatal(err)
	}
	t.Logf("install_with_backup_and_receipt elapsed=%s", time.Since(start))
	for _, item := range []struct {
		name  string
		cache *kiroStageVerificationCache
	}{{"memory", cache}, {"persistent", newKiroStageVerificationCache(root, validator, protect, unprotect)}} {
		start = time.Now()
		cached, err := prepareKiroStageInstallWith(executable, item.cache, compile)
		elapsed := time.Since(start)
		if err != nil || !cached.cacheHit || calls != 1 {
			t.Fatalf("%s cache missed: calls=%d err=%v", item.name, calls, err)
		}
		t.Logf("%s_preflight elapsed=%s compiler_calls=%d", item.name, elapsed, calls)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("real input changed: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || kiroStageSHA256(installed) != plan.candidateHash {
		t.Fatalf("installed test candidate changed: %v", err)
	}
}
