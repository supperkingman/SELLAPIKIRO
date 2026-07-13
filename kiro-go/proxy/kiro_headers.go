package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"kiro-go/config"
	"net/http"
	"strings"
)

// stableMachineID returns a stable per-account machine fingerprint.
//
// The real Kiro IDE client always sends a fixed 64-hex machine id in its
// User-Agent (KiroIDE-<ver>-<machineId>). kiro-go previously sent an EMPTY
// machine id when the account had none, so every request looked like it came
// from an unknown/mismatched device — a strong trigger for AWS's
// "USER_REQUEST_RATE_EXCEEDED / suspicious activity" throttle even though the
// account's quota is fine (9router works because it always presents a stable
// machine id). We derive a deterministic id from the account identity so it is
// stable across restarts and unique per account, without needing to persist it.
func stableMachineID(account *config.Account) string {
	if account == nil {
		return ""
	}
	if mid := strings.TrimSpace(account.MachineId); mid != "" {
		return mid
	}
	seed := strings.TrimSpace(account.ID)
	if seed == "" {
		seed = strings.TrimSpace(account.Email)
	}
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("kiro-machine:" + seed))
	return hex.EncodeToString(sum[:])
}

const (
	kiroStreamingSDKVersion = "1.0.34"
	kiroRuntimeSDKVersion   = "1.0.0"
)

type kiroHeaderValues struct {
	UserAgent    string
	AmzUserAgent string
	Host         string
}

func buildStreamingHeaderValues(account *config.Account, host string) kiroHeaderValues {
	return buildKiroHeaderValues(account, host, "codewhispererstreaming", kiroStreamingSDKVersion, "m/E")
}

func buildRuntimeHeaderValues(account *config.Account, host string) kiroHeaderValues {
	return buildKiroHeaderValues(account, host, "codewhispererruntime", kiroRuntimeSDKVersion, "m/N,E")
}

func buildKiroHeaderValues(account *config.Account, host, apiName, sdkVersion, mode string) kiroHeaderValues {
	clientCfg := config.GetKiroClientConfig()
	machineID := stableMachineID(account)

	userAgent := fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s lang/js md/nodejs#%s api/%s#%s %s KiroIDE-%s",
		sdkVersion,
		clientCfg.SystemVersion,
		clientCfg.NodeVersion,
		apiName,
		sdkVersion,
		mode,
		clientCfg.KiroVersion,
	)
	amzUserAgent := fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s", sdkVersion, clientCfg.KiroVersion)
	if machineID != "" {
		userAgent += "-" + machineID
		amzUserAgent += "-" + machineID
	}

	return kiroHeaderValues{
		UserAgent:    userAgent,
		AmzUserAgent: amzUserAgent,
		Host:         host,
	}
}

func applyKiroBaseHeaders(req *http.Request, account *config.Account, values kiroHeaderValues) {
	if account != nil && account.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+account.AccessToken)
	}
	req.Header.Set("User-Agent", values.UserAgent)
	req.Header.Set("x-amz-user-agent", values.AmzUserAgent)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	// External IdP (enterprise SSO, e.g. Azure AD) tokens MUST carry this header or
	// CodeWhisperer does not recognize the token type and silently returns an empty
	// profile list (and rejects data-plane calls). With it, a provisioned account
	// resolves its profile; an unprovisioned one gets a clear 403.
	if account != nil && account.AuthMethod == "external_idp" {
		req.Header.Set("TokenType", "EXTERNAL_IDP")
	}
	if values.Host != "" {
		req.Host = values.Host
	}
}
