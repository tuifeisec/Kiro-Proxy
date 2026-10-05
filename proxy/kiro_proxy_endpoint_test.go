package proxy

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestKiroCAInjectionEquivalentEndpoints(t *testing.T) {
	const target = "http://127.0.0.1:18081"
	for _, test := range []struct {
		current string
		want    bool
	}{
		{"http://localhost:18081", true},
		{"HTTP://LOCALHOST:18081/", true},
		{"http://127.0.0.1:18081/", true},
		{"http://127.0.0.2:18081", false},
		{"http://localhost:18082", false},
		{"https://localhost:18081", false},
		{"http://other-proxy:18081", false},
		{"http://192.168.1.20:18081", false},
		{"http://localhost:18081/other", false},
		{"", false},
		{"not a URL", false},
	} {
		t.Run(test.current, func(t *testing.T) {
			if got := shouldInjectKiroCA(test.current, target); got != test.want {
				t.Fatalf("shouldInjectKiroCA(%q, %q) = %t, want %t", test.current, target, got, test.want)
			}
		})
	}
	if !shouldInjectKiroCA("http://localhost", "http://127.0.0.1:80") {
		t.Fatal("explicit and default ports should identify the same endpoint")
	}
}

func TestKiroCAInjectionLANUsesBoundLocalAddresses(t *testing.T) {
	const target = "http://0.0.0.0:18081"
	addresses := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("192.168.1.20")},
			&net.IPAddr{IP: net.ParseIP("10.8.0.2")},
		}, nil
	}
	for _, test := range []struct {
		current string
		want    bool
	}{
		{"http://localhost:18081", true},
		{"http://127.0.0.1:18081", true},
		{"http://127.0.0.2:18081", true},
		{"http://192.168.1.20:18081", true},
		{"http://10.8.0.2:18081", true},
		{"http://192.168.1.30:18081", false},
		{"http://192.168.1.20:18082", false},
		{"https://192.168.1.20:18081", false},
		{"http://remote.example:18081", false},
	} {
		t.Run(test.current, func(t *testing.T) {
			got, err := kiroCAInjectionDecisionWithAddresses(test.current, target, addresses)
			if err != nil || got != test.want {
				t.Fatalf("LAN decision for %q = %t, %v; want %t", test.current, got, err, test.want)
			}
		})
	}
}

func TestKiroCAInjectionAddressLookupFailureIsReturned(t *testing.T) {
	wantErr := errors.New("interface lookup denied")
	called := false
	addresses := func() ([]net.Addr, error) {
		called = true
		return nil, wantErr
	}
	got, err := kiroCAInjectionDecisionWithAddresses("http://192.168.1.20:18081", "http://0.0.0.0:18081", addresses)
	if got || !errors.Is(err, wantErr) || !called {
		t.Fatalf("decision = %t, %v; called = %t", got, err, called)
	}
	called = false
	got, err = kiroCAInjectionDecisionWithAddresses("http://localhost:18081", "http://0.0.0.0:18081", addresses)
	if !got || err != nil || called {
		t.Fatalf("known loopback decision = %t, %v; lookup called = %t", got, err, called)
	}
}

func TestManualRestartHandlesProxyAliases(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1:18081", "http://0.0.0.0:18081"} {
		t.Run(target, func(t *testing.T) {
			settingsPath := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(settingsPath, []byte(`{"http.proxy":"http://localhost:18081"}`), 0600); err != nil {
				t.Fatal(err)
			}
			preparedWithCA := false
			result := restartKiroAt(nil, target, settingsPath,
				func(inject bool) (kiroLaunchPlan, error) {
					preparedWithCA = inject
					return kiroLaunchPlan{}, nil
				},
				func(kiroLaunchPlan, *TrafficLogger) error { return nil },
			)
			if !result.OK || !preparedWithCA {
				t.Fatalf("manual restart = %+v; CA injection = %t", result, preparedWithCA)
			}
		})
	}
}
