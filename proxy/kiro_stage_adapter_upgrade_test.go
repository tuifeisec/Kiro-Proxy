package proxy

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKiroStagePreviousStructuralAdapterUpgrade(t *testing.T) {
	originalPath := os.Getenv("KIRO_STAGE_TEST_BUNDLE")
	if originalPath == "" {
		originalPath = os.Getenv("KIRO_STAGE_TEST_BUNDLE_130")
	}
	if originalPath == "" {
		t.Skip("KIRO_STAGE_TEST_BUNDLE 或 KIRO_STAGE_TEST_BUNDLE_130 未设置")
	}
	original, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	previousProfiles, err := loadKiroStagePreviousStructuralProfiles()
	if err != nil {
		t.Fatal(err)
	}
	previousHash := kiroStagePreviousStructuralAdapterHash()
	oldCandidate, err := buildKiroStageStructuralCandidateWithProfiles(original, previousProfiles, previousHash)
	if err != nil {
		t.Fatalf("compile exact previous-adapter candidate: %v", err)
	}
	restored, record, found, err := parseKiroStageStructuralCandidate(oldCandidate)
	if err != nil || !found || record.AdapterSHA256 != previousHash || !bytes.Equal(restored, original) {
		t.Fatalf("old candidate is not a valid previous-profile artifact: found=%t adapter=%q err=%v", found, record.AdapterSHA256, err)
	}

	latestCandidate, err := buildKiroStageStructuralCandidate(original)
	if err != nil {
		t.Fatalf("compile latest candidate: %v", err)
	}
	if bytes.Equal(oldCandidate, latestCandidate) {
		t.Fatal("previous and latest profile artifacts unexpectedly produce identical candidates")
	}

	executable, target := writeStagePreflightFixture(t, oldCandidate)
	cacheRoot := t.TempDir()
	validator := kiroStageSHA256([]byte("previous-adapter-upgrade"))
	cache := newStagePreflightTestCache(t, cacheRoot, validator)
	compileCalls := 0
	compileLatest := func(source []byte) ([]byte, error) {
		compileCalls++
		if !bytes.Equal(source, original) {
			return nil, errors.New("latest compiler received unexpected source")
		}
		return buildKiroStageStructuralCandidate(source)
	}
	plan, err := prepareKiroStageInstallWith(executable, cache, compileLatest)
	if err != nil {
		t.Fatal(err)
	}
	if plan.cacheHit || compileCalls != 1 || plan.expectedHash != kiroStageSHA256(oldCandidate) ||
		plan.candidateHash != kiroStageSHA256(latestCandidate) || !bytes.Equal(plan.candidate, latestCandidate) {
		t.Fatalf("old adapter was not upgraded to latest candidate: calls=%d plan=%+v", compileCalls, plan)
	}
	result, err := installKiroStageBridge(plan)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(result.backupPath)
	if err != nil || !bytes.Equal(backup, oldCandidate) {
		t.Fatalf("backup does not preserve complete old candidate: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(installed, latestCandidate) {
		t.Fatalf("installed bytes differ from latest candidate: %v", err)
	}
	if _, hit, err := cache.Lookup(target, kiroStageSHA256(latestCandidate), kiroStageStructuralAdapterHash()); err != nil || !hit {
		t.Fatalf("latest-adapter receipt missing: hit=%t err=%v", hit, err)
	}
	if _, hit, err := cache.Lookup(target, kiroStageSHA256(latestCandidate), previousHash); err != nil || hit {
		t.Fatalf("upgrade receipt was recorded for previous adapter: hit=%t err=%v", hit, err)
	}

	reloaded := newStagePreflightTestCache(t, cacheRoot, validator)
	cachedPlan, err := prepareKiroStageInstallWith(executable, reloaded, func([]byte) ([]byte, error) {
		compileCalls++
		return nil, errors.New("cached upgrade unexpectedly invoked compiler")
	})
	if err != nil || !cachedPlan.cacheHit || compileCalls != 1 {
		t.Fatalf("installed latest candidate did not use persisted receipt: calls=%d plan=%+v err=%v", compileCalls, cachedPlan, err)
	}
	cachedResult, err := installKiroStageBridge(cachedPlan)
	if err != nil || cachedResult.backupPath != "" {
		t.Fatalf("idempotent cached install changed the bundle: result=%+v err=%v", cachedResult, err)
	}
	unchanged, err := os.ReadFile(originalPath)
	if err != nil || !bytes.Equal(unchanged, original) {
		t.Fatalf("real source bundle was modified: %v", err)
	}
}

func TestKiroStage20261015StructuralAdapterUpgrade(t *testing.T) {
	originalPath := os.Getenv("KIRO_STAGE_TEST_BUNDLE_130")
	if originalPath == "" {
		t.Skip("KIRO_STAGE_TEST_BUNDLE_130 is required")
	}
	original, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	oldHash := kiroStage20261015StructuralAdapterHash()
	profiles, err := loadKiroStageHistoricalStructuralProfiles(oldHash)
	if err != nil {
		t.Fatal(err)
	}
	oldCandidate, err := buildKiroStageStructuralCandidateWithProfiles(original, profiles, oldHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, record, found, err := parseKiroStageStructuralCandidate(oldCandidate); err != nil || !found || record.AdapterSHA256 != oldHash {
		t.Fatalf("old candidate identity: found=%t adapter=%q err=%v", found, record.AdapterSHA256, err)
	}
	executable, target := writeStagePreflightFixture(t, oldCandidate)
	plan, err := prepareKiroStageInstallWith(executable, nil, buildKiroStageStructuralCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if plan.expectedHash != kiroStageSHA256(oldCandidate) || len(plan.candidate) == 0 || plan.candidateHash == plan.expectedHash {
		t.Fatalf("old candidate was not upgraded: %+v", plan)
	}
	if _, record, found, err := parseKiroStageStructuralCandidate(plan.candidate); err != nil || !found || record.AdapterSHA256 != kiroStageStructuralAdapterHash() {
		t.Fatalf("new candidate identity: found=%t adapter=%q err=%v", found, record.AdapterSHA256, err)
	}
	result, err := installKiroStageBridge(plan)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(result.backupPath)
	if err != nil || !bytes.Equal(backup, oldCandidate) {
		t.Fatalf("backup does not preserve old candidate: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(installed, plan.candidate) {
		t.Fatalf("installed candidate differs: %v", err)
	}
}

func TestKiroStageStructuralAdapterRejectsUnknownIdentity(t *testing.T) {
	original := []byte("const sentinel=1;\n")
	payload := []byte("const sentinel=2;\n")
	unknownHash := kiroStageSHA256([]byte("unrecognized adapter"))
	candidate, err := wrapKiroStageStructuralCandidate(original, payload, "mock-family", unknownHash,
		[]kiroStageStructuralEdit{{Offset: len("const sentinel="), Original: "1", Installed: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	executable, target := writeStagePreflightFixture(t, candidate)
	cache := newStagePreflightTestCache(t, t.TempDir(), kiroStageSHA256([]byte("unknown-adapter")))
	compileCalls := 0
	plan, err := prepareKiroStageInstallWith(executable, cache, func([]byte) ([]byte, error) {
		compileCalls++
		return nil, errors.New("compiler must not run for unknown adapter")
	})
	if plan != nil || err == nil || !strings.Contains(err.Error(), "adapter 不受支持") || compileCalls != 0 {
		t.Fatalf("unknown adapter identity accepted: plan=%+v calls=%d err=%v", plan, compileCalls, err)
	}
	unchanged, readErr := os.ReadFile(target)
	entries, dirErr := os.ReadDir(filepath.Dir(target))
	if readErr != nil || !bytes.Equal(unchanged, candidate) || dirErr != nil || len(entries) != 1 {
		t.Fatalf("unknown adapter preflight modified installation: read=%v dir=%v entries=%d", readErr, dirErr, len(entries))
	}
}

func TestKiroStagePreviousAdapterIdentityRequiresProfileRecompilation(t *testing.T) {
	original := []byte("const sentinel=1;\n")
	payload := []byte("const sentinel=2;\n")
	oldCandidate, err := wrapKiroStageStructuralCandidate(original, payload, "forged-family", kiroStagePreviousStructuralAdapterHash(),
		[]kiroStageStructuralEdit{{Offset: len("const sentinel="), Original: "1", Installed: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := parseKiroStageStructuralCandidate(oldCandidate); err != nil || !found {
		t.Fatalf("forged fixture is not internally self-consistent: found=%t err=%v", found, err)
	}
	executable, target := writeStagePreflightFixture(t, oldCandidate)
	cache := newStagePreflightTestCache(t, t.TempDir(), kiroStageSHA256([]byte("self-consistent-old-record")))
	compileCalls := 0
	plan, err := prepareKiroStageInstallWith(executable, cache, func([]byte) ([]byte, error) {
		compileCalls++
		return nil, errors.New("latest compiler must not run after old-profile validation fails")
	})
	if plan != nil || err == nil || !strings.Contains(err.Error(), "历史 Kiro 桥接") || compileCalls != 0 {
		t.Fatalf("self-consistent record bypassed previous-profile recompilation: plan=%+v calls=%d err=%v", plan, compileCalls, err)
	}
	unchanged, readErr := os.ReadFile(target)
	entries, dirErr := os.ReadDir(filepath.Dir(target))
	if readErr != nil || !bytes.Equal(unchanged, oldCandidate) || dirErr != nil || len(entries) != 1 {
		t.Fatalf("failed previous-profile validation modified installation: read=%v dir=%v entries=%d", readErr, dirErr, len(entries))
	}
}

func TestKiroStageSelfConsistentPreviousRecordMustMatchRecompiledCandidate(t *testing.T) {
	originalPath := os.Getenv("KIRO_STAGE_TEST_BUNDLE")
	if originalPath == "" {
		originalPath = os.Getenv("KIRO_STAGE_TEST_BUNDLE_130")
	}
	if originalPath == "" {
		t.Skip("KIRO_STAGE_TEST_BUNDLE 或 KIRO_STAGE_TEST_BUNDLE_130 未设置")
	}
	original, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	previousProfiles, err := loadKiroStagePreviousStructuralProfiles()
	if err != nil {
		t.Fatal(err)
	}
	previousHash := kiroStagePreviousStructuralAdapterHash()
	validOldCandidate, err := buildKiroStageStructuralCandidateWithProfiles(original, previousProfiles, previousHash)
	if err != nil {
		t.Fatalf("original bundle must compile with the previous profile: %v", err)
	}
	payloadEnd := bytes.LastIndex(validOldCandidate, []byte(kiroStageStructuralRecordPrefix))
	if payloadEnd < 0 {
		t.Fatal("previous candidate record missing")
	}
	payload := append([]byte(nil), validOldCandidate[:payloadEnd]...)
	reconstructed, record, found, err := parseKiroStageStructuralCandidate(validOldCandidate)
	if err != nil || !found || !bytes.Equal(reconstructed, original) {
		t.Fatalf("previous candidate is not internally valid: found=%t err=%v", found, err)
	}
	record.Family = "self-consistent-but-forged-family"
	forgedCandidate := encodeKiroStageStructuralRecordForTest(t, payload, record)
	forgedOriginal, forgedRecord, found, err := parseKiroStageStructuralCandidate(forgedCandidate)
	if err != nil || !found || forgedRecord.AdapterSHA256 != previousHash || !bytes.Equal(forgedOriginal, original) {
		t.Fatalf("forged record must remain internally self-consistent: found=%t err=%v", found, err)
	}

	executable, target := writeStagePreflightFixture(t, forgedCandidate)
	cache := newStagePreflightTestCache(t, t.TempDir(), kiroStageSHA256([]byte("recompiled-old-candidate-mismatch")))
	latestCompileCalls := 0
	plan, err := prepareKiroStageInstallWith(executable, cache, func([]byte) ([]byte, error) {
		latestCompileCalls++
		return buildKiroStageStructuralCandidate(original)
	})
	if plan != nil || err == nil || !strings.Contains(err.Error(), "历史 adapter 生成候选不一致") || latestCompileCalls != 0 {
		t.Fatalf("self-consistent old record skipped full previous-candidate comparison: plan=%+v latest_calls=%d err=%v", plan, latestCompileCalls, err)
	}
	unchanged, readErr := os.ReadFile(target)
	entries, dirErr := os.ReadDir(filepath.Dir(target))
	if readErr != nil || !bytes.Equal(unchanged, forgedCandidate) || dirErr != nil || len(entries) != 1 {
		t.Fatalf("mismatched historical candidate modified installation: read=%v dir=%v entries=%d", readErr, dirErr, len(entries))
	}
}

func TestKiroStagePreviousAdapterInstallRaceDoesNotCreateBackup(t *testing.T) {
	original, candidate := stagePreflightMockCandidate(t)
	executable, target := writeStagePreflightFixture(t, original)
	cache := newStagePreflightTestCache(t, t.TempDir(), kiroStageSHA256([]byte("previous-adapter-race")))
	plan, err := prepareKiroStageInstallWith(executable, cache, func([]byte) ([]byte, error) {
		return candidate, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("changed after previous-candidate preflight")
	if err := os.WriteFile(target, changed, 0600); err != nil {
		t.Fatal(err)
	}
	entriesBefore, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installKiroStageBridge(plan); err == nil || !strings.Contains(err.Error(), "预检后发生变化") {
		t.Fatalf("changed target accepted after preflight: %v", err)
	}
	entriesAfter, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, changed) || len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("race rejection changed target or created a backup: read=%v before=%d after=%d", err, len(entriesBefore), len(entriesAfter))
	}
	if _, hit, err := cache.Lookup(target, kiroStageSHA256(candidate), kiroStageStructuralAdapterHash()); err != nil || hit {
		t.Fatalf("race rejection published a cache receipt: hit=%t err=%v", hit, err)
	}
}
