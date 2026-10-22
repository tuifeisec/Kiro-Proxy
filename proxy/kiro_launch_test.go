package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplaceEnvironmentValueRemovesDuplicatesPerPlatformRules(t *testing.T) {
	env := []string{
		"PATH=parent-path",
		"NODE_EXTRA_CA_CERTS=old-one",
		"node_extra_ca_certs=old-two",
		"OTHER=value",
	}

	windowsEnv := replaceEnvironmentValue(env, kiroExtraCACertEnv, `C:\certs\ca.crt`, true)
	assertEnvironmentValueCount(t, windowsEnv, kiroExtraCACertEnv, true, 1)
	if got := environmentValue(windowsEnv, kiroExtraCACertEnv, true); got != `C:\certs\ca.crt` {
		t.Fatalf("Windows value = %q", got)
	}

	posixEnv := replaceEnvironmentValue(env, kiroExtraCACertEnv, "/certs/ca.crt", false)
	assertEnvironmentValueCount(t, posixEnv, kiroExtraCACertEnv, false, 1)
	if got := environmentValue(posixEnv, kiroExtraCACertEnv, false); got != "/certs/ca.crt" {
		t.Fatalf("POSIX exact-key value = %q", got)
	}
	if got := environmentValue(posixEnv, "node_extra_ca_certs", false); got != "old-two" {
		t.Fatalf("POSIX lowercase-key value = %q, want old-two", got)
	}
}

func TestKiroCAInjectionDecisionUsesConfiguredProxy(t *testing.T) {
	const target = "http://127.0.0.1:18081"
	if !shouldInjectKiroCA(target, target) {
		t.Fatal("manual restart should inject CA when Kiro uses this proxy")
	}
	if shouldInjectKiroCA("http://other-proxy:8080", target) {
		t.Fatal("manual restart should not inject CA for another proxy")
	}
	if shouldInjectKiroCA("", "") {
		t.Fatal("empty target proxy must not enable CA injection")
	}
}

func TestBuildWindowsKiroLaunchPlanValidatesPathsAndUsesChildEnvironment(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "Kiro.exe")
	if err := os.WriteFile(executable, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	certPath := writeKiroTestCertificate(t, root)
	parentEnv := []string{"Path=parent-path", "NODE_EXTRA_CA_CERTS=old", "node_extra_ca_certs=older"}

	plan, err := buildWindowsKiroLaunchPlan(executable, certPath, true, parentEnv)
	if err != nil {
		t.Fatal(err)
	}
	wantExe, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	if plan.executable != wantExe {
		t.Fatalf("executable = %q, want %q", plan.executable, wantExe)
	}
	wantCert, err := filepath.Abs(certPath)
	if err != nil {
		t.Fatal(err)
	}
	assertEnvironmentValueCount(t, plan.env, kiroExtraCACertEnv, true, 1)
	if got := environmentValue(plan.env, kiroExtraCACertEnv, true); got != wantCert {
		t.Fatalf("child CA path = %q, want %q", got, wantCert)
	}
	if !plan.caInjected {
		t.Fatal("Windows launch plan must record proxy CA injection")
	}
	if got := environmentValue(parentEnv, kiroExtraCACertEnv, true); got != "older" {
		t.Fatalf("parent environment was changed: NODE_EXTRA_CA_CERTS = %q", got)
	}

	noInjection, err := buildWindowsKiroLaunchPlan(executable, filepath.Join(root, "missing.crt"), false, parentEnv)
	if err != nil {
		t.Fatal(err)
	}
	if noInjection.env != nil {
		t.Fatalf("restore launch should inherit the parent environment, got explicit child env %v", noInjection.env)
	}
	if noInjection.caInjected {
		t.Fatal("restore launch must record that proxy CA is not injected")
	}
}

func TestResolveKiroWindowsExecutableUsesKiroCLIInstallDirectory(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "Kiro")
	binDir := filepath.Join(appDir, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	cliPath := filepath.Join(binDir, "kiro.cmd")
	if err := os.WriteFile(cliPath, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	exePath := filepath.Join(appDir, "Kiro.exe")
	if err := os.WriteFile(exePath, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveKiroWindowsExecutable("", "", func(name string) (string, error) {
		if name != "kiro.cmd" {
			return "", fmt.Errorf("unexpected command lookup: %s", name)
		}
		return cliPath, nil
	}, os.Stat)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(exePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved Kiro executable = %q, want %q", got, want)
	}
}

func TestBuildDarwinKiroLaunchPlanCreatesDocumentedOpenArguments(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "Kiro.app")
	if err := os.Mkdir(appPath, 0700); err != nil {
		t.Fatal(err)
	}
	openPath := filepath.Join(root, "open")
	if err := os.WriteFile(openPath, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	certPath := writeKiroTestCertificate(t, root)

	withoutCA, err := buildDarwinKiroLaunchPlan(appPath, openPath, filepath.Join(root, "missing.crt"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(withoutCA.args, "\x00"), "-n\x00-a\x00Kiro"; got != want {
		t.Fatalf("unconfigured open args = %q, want %q", got, want)
	}
	if withoutCA.env != nil {
		t.Fatalf("open process env must inherit normally, got %v", withoutCA.env)
	}
	if withoutCA.caInjected {
		t.Fatal("launch without CA must record no CA injection")
	}

	withCA, err := buildDarwinKiroLaunchPlan(appPath, openPath, certPath, true)
	if err != nil {
		t.Fatal(err)
	}
	absCert, err := filepath.Abs(certPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-n", "--env", kiroExtraCACertEnv + "=" + absCert, "-a", "Kiro"}
	if !equalStrings(withCA.args, want) {
		t.Fatalf("configured open args = %#v, want %#v", withCA.args, want)
	}
	if !withCA.caInjected {
		t.Fatal("launch with CA must record CA injection")
	}
}

func TestManualRestartPreflightFailuresDoNotRunProcessDriver(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "Kiro.exe")
	if err := os.WriteFile(executable, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	validCert := writeKiroTestCertificate(t, root)
	settingsPath := filepath.Join(root, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"http.proxy":"http://127.0.0.1:18081"}`), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		executable string
		certPath   string
	}{
		{name: "missing certificate", executable: executable, certPath: filepath.Join(root, "missing.crt")},
		{name: "invalid certificate", executable: executable, certPath: filepath.Join(root, "invalid.crt")},
		{name: "missing executable", executable: filepath.Join(root, "missing.exe"), certPath: validCert},
	}
	if err := os.WriteFile(filepath.Join(root, "invalid.crt"), []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var launchCalled bool
			result := restartKiroAt(nil, "http://127.0.0.1:18081", settingsPath,
				func(inject bool) (kiroLaunchPlan, error) {
					if !inject {
						t.Fatal("configured proxy should enable CA validation")
					}
					return buildWindowsKiroLaunchPlan(test.executable, test.certPath, inject, os.Environ())
				},
				func(kiroLaunchPlan, *TrafficLogger) error {
					launchCalled = true
					return nil
				},
			)
			if result.OK || !strings.Contains(result.Message, "启动 Kiro 预检失败") {
				t.Fatalf("preflight result = %+v", result)
			}
			if launchCalled {
				t.Fatal("process driver ran after failed preflight")
			}
		})
	}
}

func writeKiroTestCertificate(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Kiro proxy test CA"},
		NotBefore:             time.Unix(1_700_000_000, 0),
		NotAfter:              time.Unix(2_000_000_000, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ca.crt")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertEnvironmentValueCount(t *testing.T, env []string, key string, caseInsensitive bool, want int) {
	t.Helper()
	count := 0
	for _, entry := range env {
		entryKey, _, hasValue := strings.Cut(entry, "=")
		if hasValue && environmentKeyEqual(entryKey, key, caseInsensitive) {
			count++
		}
	}
	if count != want {
		t.Fatalf("%s appears %d times in environment, want %d", key, count, want)
	}
}

func environmentValue(env []string, key string, caseInsensitive bool) string {
	value := ""
	for _, entry := range env {
		entryKey, entryValue, hasValue := strings.Cut(entry, "=")
		if hasValue && environmentKeyEqual(entryKey, key, caseInsensitive) {
			value = entryValue
		}
	}
	return value
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
