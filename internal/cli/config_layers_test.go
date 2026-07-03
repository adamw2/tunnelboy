package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoNameFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
		err  bool
	}{
		{"git@github.com:truecontext/tunnelboy-config.git", "tunnelboy-config", false},
		{"https://github.com/truecontext/tunnelboy-config", "tunnelboy-config", false},
		{"https://github.com/truecontext/tunnelboy-config.git/", "tunnelboy-config", false},
		{"git@github.com:org/..", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := repoNameFromURL(c.url)
		if c.err != (err != nil) {
			t.Errorf("repoNameFromURL(%q) err = %v, want err=%v", c.url, err, c.err)
		}
		if got != c.want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestSharedConfigs(t *testing.T) {
	home := t.TempDir()
	if got := sharedConfigs(home); len(got) != 0 {
		t.Fatalf("expected none, got %v", got)
	}
	for _, repo := range []string{"team-a", "team-b"} {
		dir := filepath.Join(home, ".tunnelboy", "shared", repo)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".tunnelboy.yaml"), []byte("profile: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := sharedConfigs(home)
	if len(got) != 2 {
		t.Fatalf("expected 2 shared configs, got %v", got)
	}
	if filepath.Base(filepath.Dir(got[0])) != "team-a" {
		t.Errorf("expected sorted order with team-a first, got %v", got)
	}
}

func TestIncludedConfigs(t *testing.T) {
	home := t.TempDir()
	inc := filepath.Join(home, "company.yaml")
	if err := os.WriteFile(inc, []byte("profile: shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeCfg := filepath.Join(home, ".tunnelboy.yaml")
	body := "includes:\n  - ~/company.yaml\n  - missing.yaml\n"
	if err := os.WriteFile(homeCfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := includedConfigs(homeCfg, home)
	if len(got) != 1 || got[0] != inc {
		t.Fatalf("expected [%s], got %v", inc, got)
	}

	if got := includedConfigs("", home); got != nil {
		t.Fatalf("expected nil for no home config, got %v", got)
	}
}
