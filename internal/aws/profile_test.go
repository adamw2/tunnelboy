package aws

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListProfilesSkipsSSOSessionSections(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	awsDir := filepath.Join(home, ".aws")
	if err := os.MkdirAll(awsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// [sso-session foo] defines a shared SSO session referenced by profiles
	// via `sso_session = foo` — it's not itself a usable --profile value.
	cfg := `[default]
region = us-east-1

[profile dev-admin]
sso_session = pf-sso-admin
region = us-east-1

[sso-session pf-sso-admin]
sso_start_url = https://example.awsapps.com/start
sso_region = us-east-1
`
	if err := os.WriteFile(filepath.Join(awsDir, "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	profiles, err := ListProfiles()
	if err != nil {
		t.Fatal(err)
	}

	names := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		names[p.Name] = true
	}

	if !names["default"] || !names["dev-admin"] {
		t.Errorf("expected default and dev-admin profiles, got %+v", profiles)
	}
	for name := range names {
		if name == "sso-session pf-sso-admin" || name == "pf-sso-admin" {
			t.Errorf("sso-session section leaked into profile list: %+v", profiles)
		}
	}
	if len(profiles) != 2 {
		t.Errorf("profiles = %+v, want exactly 2 (default, dev-admin)", profiles)
	}
}
