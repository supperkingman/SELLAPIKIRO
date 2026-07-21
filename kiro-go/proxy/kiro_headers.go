package proxy

import (
	"fmt"
	"kiro-go/config"
	"net/http"
	"os"
	"strings"
)

// stableMachineID returns a stable per-account machine fingerprint.
//
// The real Kiro IDE client always sends a fixed 64-hex machine id in its
// User-Agent (KiroIDE-<ver>-<machineId>). kiro-go previously sent an EMPTY
// machine id when the account had none, so every request looked like it came
// from an unknown/mismatched device — a strong trigger for AWS's
// "USER_REQUEST_RATE_EXCEEDED / suspicious activity" throttle even though the
// account's quota is fine.
//
// CRITICAL: the real Kiro IDE (and 9router) uses ONE machine id for the whole
// installation and reuses it for EVERY account/request — a machine identifies a
// device, not an account. AWS ties each account to the machine id it was first
// seen on; presenting a DIFFERENT machine id for an already-known account looks
// like the same credentials moving to a new device, which is exactly what
// triggers the "suspicious activity" throttle. So we must NOT derive a per-account
// id (an earlier version did, which caused throttling). Instead we use a single
// shared machine id for the whole kiro-go instance, defaulting to the same value
// 9router uses so accounts imported from 9router keep the device they were
// authorized on. Overridable per-account (account.MachineId) or globally via the
// KIRO_MACHINE_ID env var.
func stableMachineID(account *config.Account) string {
	if account != nil {
		if mid := strings.TrimSpace(account.MachineId); mid != "" {
			return mid
		}
	}
	return sharedKiroMachineID()
}

// defaultKiroMachineID matches the machine-id 9router persists at
// %APPDATA%/9router/machine-id, so accounts imported from 9router present the
// same device fingerprint they were authorized under (avoids AWS re-flagging).
const defaultKiroMachineID = "20a2bb251df6a93d3682b73f36baf4fddfb3111f3e2a63064cf59799c4599faf"

func sharedKiroMachineID() string {
	if env := strings.TrimSpace(os.Getenv("KIRO_MACHINE_ID")); env != "" {
		return env
	}
	return defaultKiroMachineID
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
	// Headless Kiro API keys (ksk_...) are sent as the bearer token, but the
	// upstream requires a "tokentype: API_KEY" header to recognize the key type.
	// Prefer the KiroApiKey field explicitly so it works even if AccessToken was
	// not mirrored for some reason.
	if account != nil && account.IsApiKeyCredential() {
		if account.KiroApiKey != "" {
			req.Header.Set("Authorization", "Bearer "+account.KiroApiKey)
		}
		req.Header.Set("tokentype", "API_KEY")
	}
	if values.Host != "" {
		req.Host = values.Host
	}
}
