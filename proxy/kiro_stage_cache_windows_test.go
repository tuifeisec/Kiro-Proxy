//go:build windows

package proxy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	kiroStageDPAPIChildMarkerEnv   = "KIRO_STAGE_DPAPI_CHILD"
	kiroStageDPAPITempRootEnv      = "KIRO_STAGE_DPAPI_TEMP_ROOT"
	kiroStageDPAPIInstalledHashEnv = "KIRO_STAGE_DPAPI_INSTALLED_HASH"
	kiroStageDPAPIOriginalHashEnv  = "KIRO_STAGE_DPAPI_ORIGINAL_HASH"
	kiroStageDPAPIAdapterHashEnv   = "KIRO_STAGE_DPAPI_ADAPTER_HASH"
	kiroStageDPAPIValidatorHashEnv = "KIRO_STAGE_DPAPI_VALIDATOR_HASH"
)

func TestKiroStageDPAPIProtectUnprotectInMemory(t *testing.T) {
	input := []byte("Kiro stage cache DPAPI test only; no files or system settings are written")
	protected, err := protectKiroStageCacheData(input)
	if err != nil {
		t.Fatalf("DPAPI protect error = %v", err)
	}
	plain, err := unprotectKiroStageCacheData(protected)
	if err != nil {
		t.Fatalf("DPAPI unprotect error = %v", err)
	}
	if !bytes.Equal(plain, input) {
		t.Fatalf("DPAPI round trip = %q, want %q", plain, input)
	}

	receipt := kiroStageVerificationReceipt{
		Schema:        kiroStageVerificationSchema,
		Target:        `C:\test\bundle.js`,
		InstalledHash: testKiroStageCacheHash("installed"),
		OriginalHash:  testKiroStageCacheHash("original"),
		AdapterHash:   testKiroStageCacheHash("adapter"),
		Validator:     testKiroStageCacheHash("validator"),
	}
	receiptPlain, err := encodeKiroStageVerificationReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	protectedReceipt, err := protectKiroStageCacheData(receiptPlain)
	if err != nil {
		t.Fatalf("DPAPI protect receipt error = %v", err)
	}
	protectedReceipt[len(protectedReceipt)/2] ^= 0xff
	cache := newKiroStageVerificationCache("", receipt.Validator, protectKiroStageCacheData, unprotectKiroStageCacheData)
	if _, err := cache.openReceipt(protectedReceipt); err == nil {
		t.Fatal("damaged DPAPI output passed receipt integrity validation")
	}
}

func TestKiroStageDPAPIPersistentAcrossChildProcess(t *testing.T) {
	tempRoot := t.TempDir()
	installedHash := testKiroStageCacheHash("fixed-child-process-installed")
	originalHash := testKiroStageCacheHash("fixed-child-process-original")
	adapterHash := testKiroStageCacheHash("fixed-child-process-adapter")
	validatorHash := testKiroStageCacheHash("fixed-child-process-validator")
	cache := newKiroStageVerificationCache(
		filepath.Join(tempRoot, "cache"),
		validatorHash,
		protectKiroStageCacheData,
		unprotectKiroStageCacheData,
	)
	target := filepath.Join(tempRoot, "bundle.js")
	if err := cache.Store(target, installedHash, originalHash, adapterHash); err != nil {
		t.Fatalf("parent Store() error = %v", err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestKiroStageDPAPIChild$")
	command.Env = append(os.Environ(),
		kiroStageDPAPIChildMarkerEnv+"=1",
		kiroStageDPAPITempRootEnv+"="+tempRoot,
		kiroStageDPAPIInstalledHashEnv+"="+installedHash,
		kiroStageDPAPIOriginalHashEnv+"="+originalHash,
		kiroStageDPAPIAdapterHashEnv+"="+adapterHash,
		kiroStageDPAPIValidatorHashEnv+"="+validatorHash,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("child-process DPAPI lookup failed: %v\n%s", err, output)
	}
}

func TestKiroStageDPAPIChild(t *testing.T) {
	if os.Getenv(kiroStageDPAPIChildMarkerEnv) != "1" {
		return
	}
	tempRoot := os.Getenv(kiroStageDPAPITempRootEnv)
	if !filepath.IsAbs(tempRoot) {
		t.Fatalf("child cache root must be an absolute temporary path: %q", tempRoot)
	}
	temporaryBase, err := filepath.Abs(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	relativeRoot, err := filepath.Rel(temporaryBase, tempRoot)
	if err != nil || relativeRoot == "." || relativeRoot == ".." || filepath.IsAbs(relativeRoot) ||
		len(relativeRoot) >= 3 && relativeRoot[:3] == ".."+string(filepath.Separator) {
		t.Fatalf("child cache root is outside the system temporary directory: %q", tempRoot)
	}

	installedHash := os.Getenv(kiroStageDPAPIInstalledHashEnv)
	originalHash := os.Getenv(kiroStageDPAPIOriginalHashEnv)
	adapterHash := os.Getenv(kiroStageDPAPIAdapterHashEnv)
	validatorHash := os.Getenv(kiroStageDPAPIValidatorHashEnv)
	if installedHash != testKiroStageCacheHash("fixed-child-process-installed") ||
		originalHash != testKiroStageCacheHash("fixed-child-process-original") ||
		adapterHash != testKiroStageCacheHash("fixed-child-process-adapter") ||
		validatorHash != testKiroStageCacheHash("fixed-child-process-validator") {
		t.Fatal("child process received unexpected verification hashes")
	}

	cache := newKiroStageVerificationCache(
		filepath.Join(tempRoot, "cache"),
		validatorHash,
		protectKiroStageCacheData,
		unprotectKiroStageCacheData,
	)
	target := filepath.Join(tempRoot, "bundle.js")
	got, hit, err := cache.Lookup(target, installedHash, adapterHash)
	if err != nil || !hit || got != originalHash {
		t.Fatalf("child Lookup() = (%q, %v, %v), want (%q, true, nil)", got, hit, err, originalHash)
	}
}
