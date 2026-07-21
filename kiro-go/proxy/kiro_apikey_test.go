package proxy

import (
	"fmt"
	"testing"

	"kiro-go/config"
)

// withStubbedProbe swaps probeKiroApiKey for the duration of a test and restores it.
func withStubbedProbe(t *testing.T, stub func(key, region string) (*config.AccountInfo, error)) {
	t.Helper()
	orig := probeKiroApiKey
	probeKiroApiKey = stub
	t.Cleanup(func() { probeKiroApiKey = orig })
}

func TestResolveApiKeyRegion_SuccessFirstRegion(t *testing.T) {
	withStubbedProbe(t, func(key, region string) (*config.AccountInfo, error) {
		return &config.AccountInfo{Email: "user@example.com", UserId: "u-1"}, nil
	})
	region, info, retryable, err := resolveApiKeyRegion("ksk_test", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retryable {
		t.Fatalf("success must not be retryable")
	}
	if region == "" {
		t.Fatalf("expected a resolved region")
	}
	if info == nil || info.UserId != "u-1" {
		t.Fatalf("expected identity from probe, got %+v", info)
	}
}

func TestResolveApiKeyRegion_ExplicitRegionSuccess(t *testing.T) {
	var probed []string
	withStubbedProbe(t, func(key, region string) (*config.AccountInfo, error) {
		probed = append(probed, region)
		return &config.AccountInfo{}, nil
	})
	region, _, _, err := resolveApiKeyRegion("ksk_test", "eu-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if region != "eu-central-1" {
		t.Fatalf("expected eu-central-1, got %s", region)
	}
	if len(probed) != 1 || probed[0] != "eu-central-1" {
		t.Fatalf("explicit region must probe only that region, probed=%v", probed)
	}
}

func TestResolveApiKeyRegion_AuthFailNotRetryable(t *testing.T) {
	withStubbedProbe(t, func(key, region string) (*config.AccountInfo, error) {
		return nil, fmt.Errorf("HTTP 403 from upstream: unauthorized")
	})
	_, _, retryable, err := resolveApiKeyRegion("ksk_bad", "")
	if err == nil {
		t.Fatalf("expected error for a key that serves no region")
	}
	if retryable {
		t.Fatalf("an all-auth-failure result must be reported as non-retryable")
	}
}

func TestResolveApiKeyRegion_TransientFailRetryable(t *testing.T) {
	withStubbedProbe(t, func(key, region string) (*config.AccountInfo, error) {
		return nil, fmt.Errorf("HTTP 503 from upstream: service unavailable")
	})
	_, _, retryable, err := resolveApiKeyRegion("ksk_test", "")
	if err == nil {
		t.Fatalf("expected error when every probe fails transiently")
	}
	if !retryable {
		t.Fatalf("a transient failure must be reported as retryable")
	}
}

func TestIsApiKeyCredential(t *testing.T) {
	cases := []struct {
		method string
		want   bool
	}{
		{"api_key", true},
		{"API_KEY", true},
		{" api_key ", true},
		{"idc", false},
		{"social", false},
		{"", false},
	}
	for _, c := range cases {
		a := &config.Account{AuthMethod: c.method}
		if got := a.IsApiKeyCredential(); got != c.want {
			t.Errorf("IsApiKeyCredential(%q) = %v, want %v", c.method, got, c.want)
		}
	}
}
