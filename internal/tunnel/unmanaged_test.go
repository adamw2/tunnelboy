package tunnel

import "testing"

func TestPortFromAddress(t *testing.T) {
	cases := map[string]int{
		"127.0.0.1:3307": 3307,
		"*:9250":         9250,
		"[::1]:3307":     3307,
		"no-colon":       0,
		"127.0.0.1:abc":  0,
	}
	for addr, want := range cases {
		if got := portFromAddress(addr); got != want {
			t.Errorf("portFromAddress(%q) = %d, want %d", addr, got, want)
		}
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"simple object", ` {"a":1} trailing`, `{"a":1}`},
		{"nested object", ` {"a":{"b":1}} trailing`, `{"a":{"b":1}}`},
		{"no object", " no braces here", ""},
		{"unbalanced", " {\"a\":1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractJSONObject(c.in); got != c.want {
				t.Errorf("extractJSONObject(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParseSSMCommand(t *testing.T) {
	t.Run("well-formed aws ssm start-session", func(t *testing.T) {
		cmd := `/opt/homebrew/bin/aws ssm start-session --region us-east-1 --profile latest ` +
			`--target ecs:cluster_task --document-name AWS-StartPortForwardingSessionToRemoteHost ` +
			`--parameters {"host":["prod-db.rds.amazonaws.com"],"portNumber":["3306"],"localPortNumber":["3307"]}`
		got := parseSSMCommand(cmd)
		want := "prod-db.rds.amazonaws.com:3306 (profile latest)"
		if got != want {
			t.Errorf("parseSSMCommand() = %q, want %q", got, want)
		}
	})

	t.Run("no profile flag", func(t *testing.T) {
		cmd := `aws ssm start-session --region us-east-1 --target i-0abc ` +
			`--parameters {"host":["10.0.0.5"],"portNumber":["22"]}`
		got := parseSSMCommand(cmd)
		want := "10.0.0.5:22"
		if got != want {
			t.Errorf("parseSSMCommand() = %q, want %q", got, want)
		}
	})

	t.Run("not an ssm command", func(t *testing.T) {
		if got := parseSSMCommand("/usr/bin/sleep 100"); got != "" {
			t.Errorf("parseSSMCommand() = %q, want empty", got)
		}
	})

	t.Run("parameters present but malformed", func(t *testing.T) {
		if got := parseSSMCommand("aws ssm start-session --parameters not-json"); got != "" {
			t.Errorf("parseSSMCommand() = %q, want empty", got)
		}
	})
}
