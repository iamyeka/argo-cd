package net

import (
	"crypto/tls"
	"net/http"
	"os"
	"testing"
	"time"
)

func withEnv(t *testing.T, key, value string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("failed to set %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestConfigureHTTP2Defaults(t *testing.T) {
	t2, err := configureHTTP2(&http.Transport{})
	if err != nil {
		t.Fatalf("configureHTTP2: %v", err)
	}
	if t2.ReadIdleTimeout != 30*time.Second {
		t.Errorf("ReadIdleTimeout = %v, want 30s", t2.ReadIdleTimeout)
	}
	if t2.PingTimeout != 15*time.Second {
		t.Errorf("PingTimeout = %v, want 15s", t2.PingTimeout)
	}
}

func TestConfigureHTTP2EnvOverride(t *testing.T) {
	withEnv(t, EnvArgoCDHTTP2ReadIdleTimeout, "5s")
	withEnv(t, EnvArgoCDHTTP2PingTimeout, "3s")
	t2, err := configureHTTP2(&http.Transport{})
	if err != nil {
		t.Fatalf("configureHTTP2: %v", err)
	}
	if t2.ReadIdleTimeout != 5*time.Second {
		t.Errorf("ReadIdleTimeout = %v, want 5s", t2.ReadIdleTimeout)
	}
	if t2.PingTimeout != 3*time.Second {
		t.Errorf("PingTimeout = %v, want 3s", t2.PingTimeout)
	}
}

func TestConfigureHTTP2EnvDisable(t *testing.T) {
	withEnv(t, EnvArgoCDHTTP2ReadIdleTimeout, "0")
	t2, err := configureHTTP2(&http.Transport{})
	if err != nil {
		t.Fatalf("configureHTTP2: %v", err)
	}
	if t2.ReadIdleTimeout != 0 {
		t.Errorf("ReadIdleTimeout = %v, want 0 (disabled)", t2.ReadIdleTimeout)
	}
}

func TestConfigureHTTP2InvalidEnvFallsBackToDefault(t *testing.T) {
	withEnv(t, EnvArgoCDHTTP2ReadIdleTimeout, "not-a-duration")
	t2, err := configureHTTP2(&http.Transport{})
	if err != nil {
		t.Fatalf("configureHTTP2: %v", err)
	}
	if t2.ReadIdleTimeout != 30*time.Second {
		t.Errorf("ReadIdleTimeout = %v, want 30s (default)", t2.ReadIdleTimeout)
	}
}

func TestSetTransportDefaultsEnablesHealthCheck(t *testing.T) {
	withEnv(t, EnvArgoCDHTTP2ReadIdleTimeout, "5s")
	tr := SetTransportDefaults(&http.Transport{TLSClientConfig: &tls.Config{}})
	if len(tr.TLSNextProto) == 0 {
		t.Fatal("http2 was not configured on the transport")
	}
}

func TestSetTransportDefaultsRespectsDisableHTTP2(t *testing.T) {
	withEnv(t, "DISABLE_HTTP2", "true")
	tr := SetTransportDefaults(&http.Transport{TLSClientConfig: &tls.Config{}})
	if len(tr.TLSNextProto) != 0 {
		t.Fatal("http2 should not be configured when DISABLE_HTTP2 is set")
	}
}
