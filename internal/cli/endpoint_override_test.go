package cli

import "testing"

func TestParseEndpointOverrideEmptyMeansDiscovery(t *testing.T) {
	got, err := parseEndpointOverride("", "", 443)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil override for an unset flag, got %+v", got)
	}
}

func TestParseEndpointOverrideUsesDefaultPort(t *testing.T) {
	got, err := parseEndpointOverride("vpc-latest-pes-os-e7psu.us-east-1.es.amazonaws.com", "", 443)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Host != "vpc-latest-pes-os-e7psu.us-east-1.es.amazonaws.com" {
		t.Errorf("host = %q", got.Host)
	}
	if got.Port != 443 {
		t.Errorf("port = %d, want 443", got.Port)
	}
	// No positional argument, so the name falls back to the first DNS label.
	if got.Name != "vpc-latest-pes-os-e7psu" {
		t.Errorf("name = %q", got.Name)
	}
}

func TestParseEndpointOverrideExplicitPortWins(t *testing.T) {
	got, err := parseEndpointOverride("db.example.com:3306", "", 443)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Host != "db.example.com" || got.Port != 3306 {
		t.Errorf("got %s:%d, want db.example.com:3306", got.Host, got.Port)
	}
}

func TestParseEndpointOverridePositionalArgumentNamesIt(t *testing.T) {
	got, err := parseEndpointOverride("vpc-latest-pes-os-e7psu.us-east-1.es.amazonaws.com", "latest-pes-os", 443)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "latest-pes-os" {
		t.Errorf("name = %q, want the positional argument", got.Name)
	}
}

func TestParseEndpointOverrideRequiresPortWhenThereIsNoDefault(t *testing.T) {
	_, err := parseEndpointOverride("db.example.com", "", 0)
	if err == nil {
		t.Fatal("expected an error when neither the flag nor a default supplies a port")
	}
}

func TestParseEndpointOverrideRejects(t *testing.T) {
	cases := map[string]string{
		"a URL":             "https://db.example.com",
		"a path":            "db.example.com/health",
		"a word for a port": "db.example.com:mysql",
		"port zero":         "db.example.com:0",
		"port above range":  "db.example.com:70000",
		"no host":           ":3306",
	}
	for name, flag := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseEndpointOverride(flag, "", 443); err == nil {
				t.Errorf("expected %q to be rejected", flag)
			}
		})
	}
}

func TestEndpointLabel(t *testing.T) {
	cases := map[string]string{
		"vpc-latest-pes-os-e7psu.us-east-1.es.amazonaws.com": "vpc-latest-pes-os-e7psu",
		"bastion":  "bastion",
		".leading": ".leading",
	}
	for host, want := range cases {
		if got := endpointLabel(host); got != want {
			t.Errorf("endpointLabel(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestFirstArg(t *testing.T) {
	if got := firstArg(nil); got != "" {
		t.Errorf("firstArg(nil) = %q", got)
	}
	if got := firstArg([]string{"one", "two"}); got != "one" {
		t.Errorf("firstArg = %q", got)
	}
}
