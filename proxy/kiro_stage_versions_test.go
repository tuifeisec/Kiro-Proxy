package proxy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKiroStageStructuralProfilesIdentity(t *testing.T) {
	const expectedHash = "3cfca7cb7f4808d82bb54141b203ab4900852afc86b1f808ac43e6a38259445d"
	if len(kiroStageStructuralProfiles) != 1890969 || kiroStageSHA256(kiroStageStructuralProfiles) != expectedHash {
		t.Fatal("restored final profiles changed byte identity")
	}
	profiles, err := loadKiroStageStructuralProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].Family != "legacy-workflow" || len(profiles[0].Hooks) != 34 ||
		profiles[1].Family != "native-model-routing" || len(profiles[1].Hooks) != 33 {
		t.Fatal("restored final profile families or hooks changed")
	}
}

func TestKiroStageManifestSelectionKeepsVersionsSeparate(t *testing.T) {
	old, err := readKiroStageManifest("kiro_stage_bridge_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	current, err := readKiroStageManifest("kiro_stage_bridge_130_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	manifests := []kiroStageManifest{old, current}
	for _, expected := range manifests {
		hashes := []string{expected.OriginalSHA256, expected.CandidateSHA256}
		for _, previous := range expected.PreviousVersions {
			hashes = append(hashes, previous.CandidateSHA256)
		}
		for _, hash := range hashes {
			got, err := selectKiroStageManifest(hash, manifests)
			if err != nil || got.OriginalSHA256 != expected.OriginalSHA256 {
				t.Fatalf("wrong manifest for %s: %+v, %v", hash, got, err)
			}
		}
	}
	if _, err := selectKiroStageManifest(kiroStageSHA256([]byte("unknown bundle")), manifests); err == nil {
		t.Fatal("unknown bundle accepted")
	}
}

func TestKiroStageUnsupportedPreflightLeavesFileUntouched(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "Kiro.exe")
	target := kiroStageBundlePath(executable)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	unknown := []byte("unknown current version")
	if err := os.WriteFile(target, unknown, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareKiroStageInstallWith(executable, nil, buildKiroStageStructuralCandidate)
	if plan != nil || err == nil || !strings.Contains(err.Error(), kiroStageSHA256(unknown)) ||
		!strings.Contains(err.Error(), "未停止或修改 Kiro") {
		t.Fatalf("unexpected result: %+v, %v", plan, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, unknown) {
		t.Fatalf("preflight changed source: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 {
		t.Fatalf("preflight created files: %v, %v", entries, err)
	}
}

func TestKiroStageLegacyRealBundleAndPreviousBridge(t *testing.T) {
	path := os.Getenv("KIRO_STAGE_TEST_BUNDLE")
	if path == "" {
		t.Skip("KIRO_STAGE_TEST_BUNDLE is required for real legacy bundle verification")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readKiroStageManifest("kiro_stage_bridge_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	current, err := buildKiroStageCandidate(original, manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, previous := range manifest.PreviousVersions {
		oldBridge := append([]byte(nil), original...)
		for _, patch := range previous.Patches {
			if bytes.Count(oldBridge, []byte(patch.Before)) != 1 {
				t.Fatalf("legacy patch anchor is not unique: %s", patch.Name)
			}
			oldBridge = bytes.Replace(oldBridge, []byte(patch.Before), []byte(patch.After), 1)
		}
		if kiroStageSHA256(oldBridge) != previous.CandidateSHA256 {
			t.Fatal("legacy bridge hash differs from registered version")
		}
		upgraded, err := buildKiroStageUpgrade(oldBridge, manifest)
		if err != nil || !bytes.Equal(upgraded, current) {
			t.Fatalf("legacy bridge upgrade regressed: %v", err)
		}
	}
}

func TestKiroStage130RealBundlePreflightAndInstall(t *testing.T) {
	path := os.Getenv("KIRO_STAGE_TEST_BUNDLE_130")
	if path == "" {
		t.Skip("KIRO_STAGE_TEST_BUNDLE_130 is required for real Kiro 1.3.0 bundle verification")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readKiroStageManifest("kiro_stage_bridge_130_manifest.json")
	if err != nil || kiroStageSHA256(original) != manifest.OriginalSHA256 {
		t.Fatalf("real source does not match 1.3.0 manifest: %v", err)
	}
	expectedCandidate, err := buildKiroStageStructuralCandidate(original)
	if err != nil {
		t.Fatalf("compile dynamic candidate: %v", err)
	}
	expectedCandidateHash := kiroStageSHA256(expectedCandidate)
	root := t.TempDir()
	executable := filepath.Join(root, "Kiro.exe")
	target := kiroStageBundlePath(executable)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	cache := newStagePreflightTestCache(t, t.TempDir(), kiroStageSHA256([]byte("version-install-test")))
	plan, err := prepareKiroStageInstallWith(executable, cache, buildKiroStageStructuralCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if plan.candidateHash != expectedCandidateHash || plan.expectedHash != kiroStageSHA256(original) ||
		plan.originalHash != kiroStageSHA256(original) || !bytes.Equal(plan.candidate, expectedCandidate) {
		t.Fatal("wrong version selected for real source")
	}
	result, err := installKiroStageBridge(plan)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(result.backupPath)
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("backup differs from complete original: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || kiroStageSHA256(installed) != expectedCandidateHash || !bytes.Equal(installed, expectedCandidate) {
		t.Fatalf("installed candidate differs: %v", err)
	}
	already, err := prepareKiroStageInstallWith(executable, cache, buildKiroStageStructuralCandidate)
	if err != nil || len(already.candidate) != 0 {
		t.Fatalf("installed bridge did not preflight idempotently: %v", err)
	}
	repeat, err := installKiroStageBridge(already)
	if err != nil || repeat.backupPath != "" {
		t.Fatalf("idempotent install rewrote bundle: %+v, %v", repeat, err)
	}
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installKiroStageBridge(already); err == nil || !strings.Contains(err.Error(), "预检后发生变化") {
		t.Fatalf("source changed after preflight was accepted: %v", err)
	}
	realAfter, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(realAfter, original) {
		t.Fatalf("real Kiro input changed: %v", err)
	}
}
