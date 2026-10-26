package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
)

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

func TestServerlessCacheTargets(t *testing.T) {
	host := "latest-cache-abc123.serverless.use1.cache.amazonaws.com"
	targets := serverlessCacheTargets([]ectypes.ServerlessCache{
		{
			ServerlessCacheName: aws.String("latest-cache"),
			Engine:              aws.String("valkey"),
			FullEngineVersion:   aws.String("9.0"),
			Status:              aws.String("available"),
			Endpoint:            &ectypes.Endpoint{Address: aws.String(host), Port: aws.Int32(6379)},
			ReaderEndpoint:      &ectypes.Endpoint{Address: aws.String(host), Port: aws.Int32(6380)},
		},
		{
			ServerlessCacheName: aws.String("creating-cache"),
			Engine:              aws.String("valkey"),
			Status:              aws.String("creating"),
		},
	})

	if len(targets) != 2 {
		t.Fatalf("expected 2 targets (endpoint and reader), got %d: %+v", len(targets), targets)
	}
	if got := targets[0]; got.Kind != "elasticache" || got.Name != "latest-cache" || got.Endpoint != host || got.Port != 6379 || got.Engine != "valkey" {
		t.Errorf("unexpected endpoint target: %+v", got)
	}
	if got := targets[1]; got.Name != "latest-cache-reader" || got.Endpoint != host || got.Port != 6380 {
		t.Errorf("unexpected reader target: %+v", got)
	}
}
