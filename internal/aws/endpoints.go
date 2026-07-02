package aws

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/kafka"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// EndpointTarget is a tunnelable endpoint discovered from a service that
// doesn't need type-specific handling beyond host:port (ElastiCache,
// DocumentDB, MSK).
type EndpointTarget struct {
	Kind     string // "elasticache", "docdb", "msk"
	Name     string
	Endpoint string
	Port     int32
	Engine   string // redis, valkey, memcached, docdb, kafka
	Detail   string // human-readable extra info for display
}

// DiscoverElastiCache discovers ElastiCache replication groups and standalone
// cache clusters (memcached, or redis clusters outside a replication group).
func (d *Discovery) DiscoverElastiCache(ctx context.Context) ([]EndpointTarget, error) {
	client := elasticache.NewFromConfig(d.cfg)
	var targets []EndpointTarget

	rgResult, err := client.DescribeReplicationGroups(ctx, &elasticache.DescribeReplicationGroupsInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to describe ElastiCache replication groups: %w", err)
	}

	for _, rg := range rgResult.ReplicationGroups {
		engine := aws.ToString(rg.Engine)
		if engine == "" {
			engine = "redis"
		}
		target := EndpointTarget{
			Kind:   "elasticache",
			Name:   aws.ToString(rg.ReplicationGroupId),
			Engine: engine,
			Detail: fmt.Sprintf("%s  %d node group(s)", engine, len(rg.NodeGroups)),
		}

		// Cluster mode on: configuration endpoint. Otherwise: primary endpoint
		// of the first node group.
		if rg.ConfigurationEndpoint != nil {
			target.Endpoint = aws.ToString(rg.ConfigurationEndpoint.Address)
			target.Port = aws.ToInt32(rg.ConfigurationEndpoint.Port)
		} else if len(rg.NodeGroups) > 0 && rg.NodeGroups[0].PrimaryEndpoint != nil {
			target.Endpoint = aws.ToString(rg.NodeGroups[0].PrimaryEndpoint.Address)
			target.Port = aws.ToInt32(rg.NodeGroups[0].PrimaryEndpoint.Port)
		}

		if target.Endpoint != "" {
			targets = append(targets, target)
		}
	}

	// Standalone clusters (memcached; redis nodes not in a replication group)
	ccResult, err := client.DescribeCacheClusters(ctx, &elasticache.DescribeCacheClustersInput{
		ShowCacheNodeInfo: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe ElastiCache clusters: %w", err)
	}

	for _, cc := range ccResult.CacheClusters {
		if cc.ReplicationGroupId != nil {
			continue // already covered above
		}
		target := EndpointTarget{
			Kind:   "elasticache",
			Name:   aws.ToString(cc.CacheClusterId),
			Engine: aws.ToString(cc.Engine),
			Detail: fmt.Sprintf("%s %s  %s", aws.ToString(cc.Engine), aws.ToString(cc.EngineVersion), aws.ToString(cc.CacheNodeType)),
		}
		if cc.ConfigurationEndpoint != nil {
			target.Endpoint = aws.ToString(cc.ConfigurationEndpoint.Address)
			target.Port = aws.ToInt32(cc.ConfigurationEndpoint.Port)
		} else if len(cc.CacheNodes) > 0 && cc.CacheNodes[0].Endpoint != nil {
			target.Endpoint = aws.ToString(cc.CacheNodes[0].Endpoint.Address)
			target.Port = aws.ToInt32(cc.CacheNodes[0].Endpoint.Port)
		}
		if target.Endpoint != "" {
			targets = append(targets, target)
		}
	}

	return targets, nil
}

// DiscoverDocDBClusters discovers DocumentDB clusters. DocumentDB shares the
// RDS API surface, so no extra SDK module is needed.
func (d *Discovery) DiscoverDocDBClusters(ctx context.Context) ([]EndpointTarget, error) {
	client := rds.NewFromConfig(d.cfg)

	result, err := client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to describe DB clusters: %w", err)
	}

	var targets []EndpointTarget
	for _, cluster := range result.DBClusters {
		if aws.ToString(cluster.Engine) != "docdb" {
			continue
		}
		targets = append(targets, EndpointTarget{
			Kind:     "docdb",
			Name:     aws.ToString(cluster.DBClusterIdentifier),
			Endpoint: aws.ToString(cluster.Endpoint),
			Port:     aws.ToInt32(cluster.Port),
			Engine:   "docdb",
			Detail:   fmt.Sprintf("docdb %s  %s", aws.ToString(cluster.EngineVersion), aws.ToString(cluster.Status)),
		})
	}

	return targets, nil
}

// DiscoverMSKClusters discovers MSK clusters, resolving the first bootstrap
// broker as the tunnel endpoint (preferring IAM > TLS > SCRAM > plaintext
// listeners).
func (d *Discovery) DiscoverMSKClusters(ctx context.Context) ([]EndpointTarget, error) {
	client := kafka.NewFromConfig(d.cfg)

	result, err := client.ListClustersV2(ctx, &kafka.ListClustersV2Input{})
	if err != nil {
		return nil, fmt.Errorf("failed to list MSK clusters: %w", err)
	}

	var targets []EndpointTarget
	for _, cluster := range result.ClusterInfoList {
		arn := aws.ToString(cluster.ClusterArn)
		brokers, err := client.GetBootstrapBrokers(ctx, &kafka.GetBootstrapBrokersInput{
			ClusterArn: aws.String(arn),
		})
		if err != nil {
			continue // cluster may still be creating
		}

		brokerString, listener := pickBrokerString(
			aws.ToString(brokers.BootstrapBrokerStringSaslIam),
			aws.ToString(brokers.BootstrapBrokerStringTls),
			aws.ToString(brokers.BootstrapBrokerStringSaslScram),
			aws.ToString(brokers.BootstrapBrokerString),
		)
		host, port, err := parseFirstBroker(brokerString)
		if err != nil {
			continue
		}

		targets = append(targets, EndpointTarget{
			Kind:     "msk",
			Name:     aws.ToString(cluster.ClusterName),
			Endpoint: host,
			Port:     port,
			Engine:   "kafka",
			Detail:   fmt.Sprintf("%s  %s listener  %s", cluster.ClusterType, listener, aws.ToString(cluster.ClusterArn)),
		})
	}

	return targets, nil
}

// pickBrokerString returns the most secure non-empty bootstrap broker string
// and a label for which listener it belongs to.
func pickBrokerString(saslIam, tls, saslScram, plaintext string) (string, string) {
	switch {
	case saslIam != "":
		return saslIam, "SASL/IAM"
	case tls != "":
		return tls, "TLS"
	case saslScram != "":
		return saslScram, "SASL/SCRAM"
	default:
		return plaintext, "PLAINTEXT"
	}
}

// parseFirstBroker extracts host and port from the first entry of a
// comma-separated bootstrap broker string ("b-1.x.kafka...:9098,b-2...").
func parseFirstBroker(brokers string) (string, int32, error) {
	first := strings.TrimSpace(strings.Split(brokers, ",")[0])
	if first == "" {
		return "", 0, fmt.Errorf("empty broker string")
	}
	idx := strings.LastIndex(first, ":")
	if idx <= 0 || idx == len(first)-1 {
		return "", 0, fmt.Errorf("malformed broker endpoint %q", first)
	}
	port, err := strconv.Atoi(first[idx+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("malformed broker port in %q", first)
	}
	return first[:idx], int32(port), nil // #nosec G115 G109 -- port bounds-checked to 1..65535 above
}
