package aws

import (
	"fmt"
	"strings"
)

// credentialErrorMarkers are substrings that identify expired, missing, or
// otherwise unusable AWS credentials across the SDK's error surfaces (SSO
// token cache, STS, env creds from Granted, service-level auth failures).
var credentialErrorMarkers = []string{
	"expiredtoken",
	"expiredtokenexception",
	"token included in the request is expired",
	"security token included in the request is invalid",
	"failed to refresh cached credentials",
	"sso session has expired",
	"sso session is invalid",
	"invalidgrantexception",
	"unauthorizedexception",
	"token has expired",
	"no ec2 imds role found",
	"failed to retrieve credentials",
	"static credentials are empty",
	"unable to load sso token",
	"invalidclienttokenid",
}

// IsCredentialError reports whether err looks like an AWS credential problem
// (expired SSO session, stale env creds, missing profile creds) rather than a
// genuine service or network failure.
func IsCredentialError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range credentialErrorMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// CredentialHint returns a user-facing message explaining how to refresh
// credentials for the given profile.
func CredentialHint(profile string) string {
	if profile == "" {
		profile = "<profile>"
	}
	return fmt.Sprintf(
		"AWS credentials are missing or expired.\n\nRefresh them and retry:\n  assume %s        (Granted SSO)\n  aws sso login --profile %s",
		profile, profile)
}
