package aws

import "testing"

func TestParseFirstBroker(t *testing.T) {
	host, port, err := parseFirstBroker("b-1.mycluster.abc123.c2.kafka.us-east-1.amazonaws.com:9098,b-2.mycluster.abc123.c2.kafka.us-east-1.amazonaws.com:9098")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "b-1.mycluster.abc123.c2.kafka.us-east-1.amazonaws.com" || port != 9098 {
		t.Errorf("got %s:%d", host, port)
	}

	for _, bad := range []string{"", "no-port", ":9092", "host:"} {
		if _, _, err := parseFirstBroker(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestPickBrokerString(t *testing.T) {
	s, label := pickBrokerString("iam:9098", "tls:9094", "scram:9096", "plain:9092")
	if s != "iam:9098" || label != "SASL/IAM" {
		t.Errorf("got %s %s", s, label)
	}
	s, label = pickBrokerString("", "", "", "plain:9092")
	if s != "plain:9092" || label != "PLAINTEXT" {
		t.Errorf("got %s %s", s, label)
	}
}
