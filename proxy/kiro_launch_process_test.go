package proxy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKiroLaunchSubprocessEnvironment(t *testing.T) {
	if os.Getenv("KIRO_LAUNCH_CHILD_HELPER") == "1" {
		outputPath := os.Getenv("KIRO_LAUNCH_CHILD_OUTPUT")
		if outputPath == "" {
			t.Fatal("child environment output path is missing")
		}
		contents := os.Getenv(kiroExtraCACertEnv) + "\n" + os.Getenv("PATH")
		if err := os.WriteFile(outputPath, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}

	parentCert, parentHasCert := os.LookupEnv(kiroExtraCACertEnv)
	parentPath := os.Getenv("PATH")
	outputPath := filepath.Join(t.TempDir(), "child-env.txt")
	wantCert := filepath.Join(t.TempDir(), "isolated cert.pem")
	caseInsensitive := runtime.GOOS == "windows"
	childEnv := replaceEnvironmentValue(os.Environ(), "KIRO_LAUNCH_CHILD_HELPER", "1", caseInsensitive)
	childEnv = replaceEnvironmentValue(childEnv, "KIRO_LAUNCH_CHILD_OUTPUT", outputPath, caseInsensitive)
	childEnv = replaceEnvironmentValue(childEnv, kiroExtraCACertEnv, wantCert, runtime.GOOS == "windows")
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	plan := kiroLaunchPlan{
		platform:   "darwin",
		executable: executable,
		args:       []string{"-test.run=^TestKiroLaunchSubprocessEnvironment$"},
		env:        childEnv,
	}
	if err := startKiroProcess(plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	values := strings.SplitN(string(data), "\n", 2)
	if len(values) != 2 || values[0] != wantCert || values[1] != parentPath {
		t.Fatalf("child environment values = %q, want CA %q and inherited PATH %q", data, wantCert, parentPath)
	}
	afterCert, afterHasCert := os.LookupEnv(kiroExtraCACertEnv)
	if afterHasCert != parentHasCert || afterCert != parentCert {
		t.Fatalf("parent environment changed: before (%q,%t), after (%q,%t)", parentCert, parentHasCert, afterCert, afterHasCert)
	}
}
