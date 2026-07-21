package proxy

import (
	"fmt"
	"os"
	"strings"

	"kiro-go/config"
	"kiro-go/pool"
)

// kiroApiKeyCandidateRegions is the ordered set of data-plane regions probed when a
// Kiro API key is added without an explicit region. Defaults to the same set as
// OAuth profile-region discovery (defaultKiroProfileRegions); the KIRO_PROFILE_REGIONS
// env var (comma-separated) overrides it, sharing that single knob.
func kiroApiKeyCandidateRegions() []string {
	if env := strings.TrimSpace(os.Getenv("KIRO_PROFILE_REGIONS")); env != "" {
		var out []string
		for _, r := range strings.Split(env, ",") {
			if r = strings.TrimSpace(r); r != "" {
				out = append(out, r)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return defaultKiroProfileRegions
}

// probeKiroApiKey validates the key in a specific data-plane region and returns the
// underlying Kiro account info (identity + subscription + usage) fetched in the same
// call. It uses a throwaway in-memory account (never persisted). A non-nil error
// means the key does not serve that region. It is a package var so tests can stub
// the upstream round-trip.
var probeKiroApiKey = func(key, region string) (*config.AccountInfo, error) {
	acc := &config.Account{
		KiroApiKey:  key,
		AccessToken: key,
		AuthMethod:  "api_key",
		Region:      region,
	}
	info, err := RefreshAccountInfo(acc)
	if err != nil {
		return nil, err
	}
	return info, nil
}

// resolveApiKeyRegion determines the data-plane region a ksk_ key actually serves,
// returning the region plus the account identity the probe fetched on the way.
//
// The Kiro profile is bound to the key server-side, but the data-plane endpoint is
// regional, so a key only answers in its home region. A wrong region is
// unrecoverable: api_key accounts never re-probe (ResolveProfileArn short-circuits
// for key-bound profiles). So the region is never assumed — an explicit region
// narrows the probe set to that one region, and an empty region probes every
// candidate.
//
// The bool reports whether the failure looks retryable: false means every probe was
// an auth rejection (the key genuinely does not serve those regions — a caller
// error), true means at least one probe failed for a transient reason (upstream 5xx,
// timeout, proxy outage), which must not be reported as a bad key.
func resolveApiKeyRegion(key, explicitRegion string) (string, *config.AccountInfo, bool, error) {
	targetRegions := kiroApiKeyCandidateRegions()
	if explicit := strings.TrimSpace(explicitRegion); explicit != "" {
		targetRegions = []string{explicit}
	}

	var errs []string
	retryable := false
	for _, region := range targetRegions {
		info, err := probeKiroApiKey(key, region)
		if err == nil {
			return region, info, false, nil
		}
		if !pool.IsAuthFailure(err) {
			retryable = true
		}
		errs = append(errs, region+": "+err.Error())
	}
	return "", nil, retryable, fmt.Errorf("kiroApiKey not usable in any probed region (%s)", strings.Join(errs, "; "))
}
