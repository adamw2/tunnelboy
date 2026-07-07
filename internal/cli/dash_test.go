package cli

import (
	"testing"

	"github.com/adamw2/tunnelboy/internal/state"
)

func TestConnectionString(t *testing.T) {
	cases := []struct {
		name string
		st   state.TunnelState
		want string
	}{
		{"rds postgres", state.TunnelState{Type: "rds", Engine: "aurora-postgresql", LocalPort: 15432},
			"postgresql://localhost:15432/"},
		{"rds mysql", state.TunnelState{Type: "rds", Engine: "mysql", LocalPort: 13306},
			"mysql://localhost:13306/"},
		{"rds unknown engine (pre-engine state file)", state.TunnelState{Type: "rds", LocalPort: 15432},
			"localhost:15432"},
		{"opensearch", state.TunnelState{Type: "opensearch", LocalPort: 9250},
			"http://localhost:9250"},
		{"elasticache redis", state.TunnelState{Type: "elasticache", Engine: "redis", LocalPort: 16379},
			"redis://localhost:16379"},
		{"elasticache valkey", state.TunnelState{Type: "elasticache", Engine: "valkey", LocalPort: 16379},
			"redis://localhost:16379"},
		{"elasticache memcached", state.TunnelState{Type: "elasticache", Engine: "memcached", LocalPort: 11211},
			"localhost:11211"},
		{"docdb", state.TunnelState{Type: "docdb", LocalPort: 27017},
			"mongodb://localhost:27017/?tls=true&tlsAllowInvalidHostnames=true&directConnection=true&retryWrites=false"},
		{"ec2", state.TunnelState{Type: "ec2", LocalPort: 12222},
			"localhost:12222"},
		{"msk", state.TunnelState{Type: "msk", LocalPort: 19092},
			"localhost:19092"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := connectionString(c.st); got != c.want {
				t.Errorf("connectionString(%+v) = %q, want %q", c.st, got, c.want)
			}
		})
	}
}
