package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/yourorg/tunnelboy/internal/config"
)

// Discovery handles AWS resource discovery
type Discovery struct {
	cfg aws.Config
}

// NewDiscovery creates a new discovery client
func NewDiscovery(cfg aws.Config) *Discovery {
	return &Discovery{cfg: cfg}
}

// JumpHost represents a unified jump host (EC2 or ECS)
type JumpHost struct {
	ID          string // Instance ID or ECS task ARN
	Name        string
	Type        string // "ec2" or "ecs"
	PrivateIP   string
	SSMEnabled  bool
	ClusterName string // For ECS
	Tags        map[string]string
}

// EC2Instance represents a discovered EC2 instance
type EC2Instance struct {
	InstanceID   string
	Name         string
	PrivateIP    string
	PublicIP     string
	InstanceType string
	State        string
	SSMEnabled   bool
	Tags         map[string]string
}

// RDSInstance represents a discovered RDS instance
type RDSInstance struct {
	Identifier   string
	Engine       string
	EngineVersion string
	InstanceClass string
	Endpoint     string
	Port         int32
	Status       string
	VpcID        string
}

// OpenSearchDomain represents a discovered OpenSearch domain
type OpenSearchDomain struct {
	DomainName  string
	Endpoint    string
	EngineVersion string
	InstanceType string
	InstanceCount int
	VpcID       string
}

// ECSTask represents a discovered ECS task
type ECSTask struct {
	TaskARN        string
	TaskID         string
	ClusterARN     string
	ClusterName    string
	ServiceName    string
	TaskDefinition string
	Status         string
	PrivateIP      string
	RuntimeID      string // Container runtime ID for SSM
	SSMTarget      string // Formatted target for SSM: ecs:cluster_taskid_runtimeid
}

// DiscoverEC2Instances discovers EC2 instances
func (d *Discovery) DiscoverEC2Instances(ctx context.Context) ([]EC2Instance, error) {
	client := ec2.NewFromConfig(d.cfg)

	// Get all running instances
	input := &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running"},
			},
		},
	}

	result, err := client.DescribeInstances(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe instances: %w", err)
	}

	// Get SSM managed instances
	ssmEnabled, err := d.getSSMManagedInstances(ctx)
	if err != nil {
		// Non-fatal, just means we can't determine SSM status
		ssmEnabled = make(map[string]bool)
	}

	var instances []EC2Instance
	for _, reservation := range result.Reservations {
		for _, inst := range reservation.Instances {
			instance := EC2Instance{
				InstanceID:   aws.ToString(inst.InstanceId),
				PrivateIP:    aws.ToString(inst.PrivateIpAddress),
				PublicIP:     aws.ToString(inst.PublicIpAddress),
				InstanceType: string(inst.InstanceType),
				State:        string(inst.State.Name),
				Tags:         make(map[string]string),
				SSMEnabled:   ssmEnabled[aws.ToString(inst.InstanceId)],
			}

			// Extract tags
			for _, tag := range inst.Tags {
				key := aws.ToString(tag.Key)
				value := aws.ToString(tag.Value)
				instance.Tags[key] = value
				if key == "Name" {
					instance.Name = value
				}
			}

			instances = append(instances, instance)
		}
	}

	return instances, nil
}

// DiscoverJumpHosts discovers jump hosts (EC2 and ECS) based on config
func (d *Discovery) DiscoverJumpHosts(ctx context.Context, cfg *config.Config) ([]JumpHost, error) {
	var jumpHosts []JumpHost

	// Get patterns
	patterns := cfg.JumpHosts.Patterns
	if len(patterns) == 0 {
		patterns = []string{"*bastion*", "*jump*"}
	}

	// Check explicit instances first
	if len(cfg.JumpHosts.Instances) > 0 {
		allInstances, err := d.DiscoverEC2Instances(ctx)
		if err != nil {
			return nil, err
		}

		explicitSet := make(map[string]bool)
		for _, id := range cfg.JumpHosts.Instances {
			explicitSet[id] = true
		}
		for _, inst := range allInstances {
			if explicitSet[inst.InstanceID] {
				jumpHosts = append(jumpHosts, JumpHost{
					ID:         inst.InstanceID,
					Name:       inst.Name,
					Type:       "ec2",
					PrivateIP:  inst.PrivateIP,
					SSMEnabled: inst.SSMEnabled,
					Tags:       inst.Tags,
				})
			}
		}
		if len(jumpHosts) > 0 {
			return jumpHosts, nil
		}
	}

	// Check explicit ECS tasks
	if len(cfg.JumpHosts.ECS) > 0 {
		for _, ecsConfig := range cfg.JumpHosts.ECS {
			tasks, err := d.discoverECSTasksByService(ctx, ecsConfig.Cluster, ecsConfig.Service)
			if err != nil {
				continue
			}
			for _, task := range tasks {
				// Use SSMTarget if available
				targetID := task.SSMTarget
				if targetID == "" {
					targetID = task.TaskARN
				}
				
				jumpHosts = append(jumpHosts, JumpHost{
					ID:          targetID,
					Name:        task.ServiceName,
					Type:        "ecs",
					PrivateIP:   task.PrivateIP,
					ClusterName: task.ClusterName,
					SSMEnabled:  task.SSMTarget != "", // Only enabled if we have runtime ID
				})
			}
		}
		if len(jumpHosts) > 0 {
			return jumpHosts, nil
		}
	}

	// Discover EC2 instances by tags
	allInstances, err := d.DiscoverEC2Instances(ctx)
	if err != nil {
		return nil, err
	}

	if len(cfg.JumpHosts.Tags) > 0 {
		for _, inst := range allInstances {
			for _, tagFilter := range cfg.JumpHosts.Tags {
				if inst.Tags[tagFilter.Key] == tagFilter.Value {
					jumpHosts = append(jumpHosts, JumpHost{
						ID:         inst.InstanceID,
						Name:       inst.Name,
						Type:       "ec2",
						PrivateIP:  inst.PrivateIP,
						SSMEnabled: inst.SSMEnabled,
						Tags:       inst.Tags,
					})
					break
				}
			}
		}
		if len(jumpHosts) > 0 {
			return jumpHosts, nil
		}
	}

	// Discover by patterns (both EC2 and ECS)
	// Check EC2 instances
	for _, inst := range allInstances {
		for _, pattern := range patterns {
			if matchPattern(inst.Name, pattern) {
				jumpHosts = append(jumpHosts, JumpHost{
					ID:         inst.InstanceID,
					Name:       inst.Name,
					Type:       "ec2",
					PrivateIP:  inst.PrivateIP,
					SSMEnabled: inst.SSMEnabled,
					Tags:       inst.Tags,
				})
				break
			}
		}
	}

	// Check ECS tasks
	ecsTasks, _ := d.DiscoverECSTasks(ctx, patterns)
	for _, task := range ecsTasks {
		// Use SSMTarget if available, otherwise fall back to task ARN
		targetID := task.SSMTarget
		if targetID == "" {
			targetID = task.TaskARN
		}
		
		jumpHosts = append(jumpHosts, JumpHost{
			ID:          targetID,
			Name:        fmt.Sprintf("%s/%s", task.ClusterName, task.TaskDefinition),
			Type:        "ecs",
			PrivateIP:   task.PrivateIP,
			ClusterName: task.ClusterName,
			SSMEnabled:  task.SSMTarget != "", // Only enabled if we have runtime ID
		})
	}

	return jumpHosts, nil
}

// discoverECSTasksByService discovers ECS tasks in a specific service
func (d *Discovery) discoverECSTasksByService(ctx context.Context, clusterName, serviceName string) ([]ECSTask, error) {
	client := ecs.NewFromConfig(d.cfg)

	// List tasks for the service
	tasksResult, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       aws.String(clusterName),
		ServiceName:   aws.String(serviceName),
		DesiredStatus: "RUNNING",
	})
	if err != nil {
		return nil, err
	}

	if len(tasksResult.TaskArns) == 0 {
		return nil, nil
	}

	// Describe tasks
	descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   tasksResult.TaskArns,
	})
	if err != nil {
		return nil, err
	}

	var tasks []ECSTask
	for _, task := range descResult.Tasks {
		taskID := extractTaskID(aws.ToString(task.TaskArn))
		
		ecsTask := ECSTask{
			TaskARN:        aws.ToString(task.TaskArn),
			TaskID:         taskID,
			ClusterARN:     clusterName,
			ClusterName:    clusterName,
			ServiceName:    serviceName,
			TaskDefinition: extractTaskFamily(aws.ToString(task.TaskDefinitionArn)),
			Status:         aws.ToString(task.LastStatus),
		}

		// Get private IP from network attachments
		for _, attachment := range task.Attachments {
			if aws.ToString(attachment.Type) == "ElasticNetworkInterface" {
				for _, detail := range attachment.Details {
					if aws.ToString(detail.Name) == "privateIPv4Address" {
						ecsTask.PrivateIP = aws.ToString(detail.Value)
					}
				}
			}
		}

		// Get runtime ID from first container (for SSM)
		if len(task.Containers) > 0 {
			// The runtime ID is the last part of the container runtime ID
			runtimeID := aws.ToString(task.Containers[0].RuntimeId)
			if runtimeID != "" {
				ecsTask.RuntimeID = runtimeID
				// Format: ecs:cluster_taskid_runtimeid
				ecsTask.SSMTarget = fmt.Sprintf("ecs:%s_%s_%s", 
					extractClusterName(clusterName),
					taskID,
					runtimeID)
			}
		}

		tasks = append(tasks, ecsTask)
	}

	return tasks, nil
}

// DiscoverRDSInstances discovers RDS instances
func (d *Discovery) DiscoverRDSInstances(ctx context.Context) ([]RDSInstance, error) {
	client := rds.NewFromConfig(d.cfg)

	result, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to describe RDS instances: %w", err)
	}

	var instances []RDSInstance
	for _, db := range result.DBInstances {
		instance := RDSInstance{
			Identifier:    aws.ToString(db.DBInstanceIdentifier),
			Engine:        aws.ToString(db.Engine),
			EngineVersion: aws.ToString(db.EngineVersion),
			InstanceClass: aws.ToString(db.DBInstanceClass),
			Status:        aws.ToString(db.DBInstanceStatus),
		}

		if db.Endpoint != nil {
			instance.Endpoint = aws.ToString(db.Endpoint.Address)
			instance.Port = aws.ToInt32(db.Endpoint.Port)
		}

		if db.DBSubnetGroup != nil {
			instance.VpcID = aws.ToString(db.DBSubnetGroup.VpcId)
		}

		instances = append(instances, instance)
	}

	return instances, nil
}

// DiscoverOpenSearchDomains discovers OpenSearch domains
func (d *Discovery) DiscoverOpenSearchDomains(ctx context.Context) ([]OpenSearchDomain, error) {
	client := opensearch.NewFromConfig(d.cfg)

	// List domain names
	listResult, err := client.ListDomainNames(ctx, &opensearch.ListDomainNamesInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to list OpenSearch domains: %w", err)
	}

	if len(listResult.DomainNames) == 0 {
		return nil, nil
	}

	// Get domain names
	domainNames := make([]string, 0, len(listResult.DomainNames))
	for _, d := range listResult.DomainNames {
		domainNames = append(domainNames, aws.ToString(d.DomainName))
	}

	// Describe domains
	descResult, err := client.DescribeDomains(ctx, &opensearch.DescribeDomainsInput{
		DomainNames: domainNames,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe OpenSearch domains: %w", err)
	}

	var domains []OpenSearchDomain
	for _, ds := range descResult.DomainStatusList {
		domain := OpenSearchDomain{
			DomainName:    aws.ToString(ds.DomainName),
			EngineVersion: aws.ToString(ds.EngineVersion),
		}

		if ds.Endpoints != nil {
			if vpc, ok := ds.Endpoints["vpc"]; ok {
				domain.Endpoint = vpc
			}
		}
		if domain.Endpoint == "" && ds.Endpoint != nil {
			domain.Endpoint = aws.ToString(ds.Endpoint)
		}

		if ds.ClusterConfig != nil {
			domain.InstanceType = string(ds.ClusterConfig.InstanceType)
			domain.InstanceCount = int(aws.ToInt32(ds.ClusterConfig.InstanceCount))
		}

		if ds.VPCOptions != nil {
			domain.VpcID = aws.ToString(ds.VPCOptions.VPCId)
		}

		domains = append(domains, domain)
	}

	return domains, nil
}

// DiscoverECSTasks discovers ECS tasks matching patterns
func (d *Discovery) DiscoverECSTasks(ctx context.Context, patterns []string) ([]ECSTask, error) {
	client := ecs.NewFromConfig(d.cfg)

	// List clusters
	clustersResult, err := client.ListClusters(ctx, &ecs.ListClustersInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to list ECS clusters: %w", err)
	}

	var tasks []ECSTask

	for _, clusterARN := range clustersResult.ClusterArns {
		// List tasks in cluster
		tasksResult, err := client.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster:       aws.String(clusterARN),
			DesiredStatus: "RUNNING",
		})
		if err != nil {
			continue // Skip clusters we can't access
		}

		if len(tasksResult.TaskArns) == 0 {
			continue
		}

		// Describe tasks
		descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterARN),
			Tasks:   tasksResult.TaskArns,
		})
		if err != nil {
			continue
		}

		for _, task := range descResult.Tasks {
			// Extract task definition family
			taskDefARN := aws.ToString(task.TaskDefinitionArn)
			family := extractTaskFamily(taskDefARN)

			// Check if matches any pattern
			matches := false
			for _, pattern := range patterns {
				if matchPattern(family, pattern) {
					matches = true
					break
				}
			}

			if !matches {
				continue
			}

			ecsTask := ECSTask{
				TaskARN:        aws.ToString(task.TaskArn),
				TaskID:         extractTaskID(aws.ToString(task.TaskArn)),
				ClusterARN:     clusterARN,
				ClusterName:    extractClusterName(clusterARN),
				TaskDefinition: family,
				Status:         aws.ToString(task.LastStatus),
			}

			// Get private IP from network interfaces
			for _, attachment := range task.Attachments {
				if aws.ToString(attachment.Type) == "ElasticNetworkInterface" {
					for _, detail := range attachment.Details {
						if aws.ToString(detail.Name) == "privateIPv4Address" {
							ecsTask.PrivateIP = aws.ToString(detail.Value)
						}
					}
				}
			}

			// Get runtime ID from first container (for SSM)
			if len(task.Containers) > 0 {
				runtimeID := aws.ToString(task.Containers[0].RuntimeId)
				if runtimeID != "" {
					ecsTask.RuntimeID = runtimeID
					// Format: ecs:cluster_taskid_runtimeid
					ecsTask.SSMTarget = fmt.Sprintf("ecs:%s_%s_%s",
						extractClusterName(clusterARN),
						ecsTask.TaskID,
						runtimeID)
				}
			}

			tasks = append(tasks, ecsTask)
		}
	}

	return tasks, nil
}

// getSSMManagedInstances returns a map of instance IDs that are SSM managed
func (d *Discovery) getSSMManagedInstances(ctx context.Context) (map[string]bool, error) {
	client := ssm.NewFromConfig(d.cfg)

	result, err := client.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{})
	if err != nil {
		return nil, err
	}

	managed := make(map[string]bool)
	for _, info := range result.InstanceInformationList {
		managed[aws.ToString(info.InstanceId)] = true
	}

	return managed, nil
}

// matchPattern performs simple glob matching
func matchPattern(s, pattern string) bool {
	s = strings.ToLower(s)
	pattern = strings.ToLower(pattern)

	// Simple glob: only supports * at start and/or end
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		// *foo* - contains
		substr := pattern[1 : len(pattern)-1]
		return strings.Contains(s, substr)
	} else if strings.HasPrefix(pattern, "*") {
		// *foo - ends with
		suffix := pattern[1:]
		return strings.HasSuffix(s, suffix)
	} else if strings.HasSuffix(pattern, "*") {
		// foo* - starts with
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(s, prefix)
	}
	// Exact match
	return s == pattern
}

func extractTaskFamily(taskDefARN string) string {
	// arn:aws:ecs:region:account:task-definition/family:revision
	parts := strings.Split(taskDefARN, "/")
	if len(parts) < 2 {
		return taskDefARN
	}
	familyRev := parts[len(parts)-1]
	// Remove revision
	colonIdx := strings.LastIndex(familyRev, ":")
	if colonIdx > 0 {
		return familyRev[:colonIdx]
	}
	return familyRev
}

func extractTaskID(taskARN string) string {
	// arn:aws:ecs:region:account:task/cluster/task-id
	parts := strings.Split(taskARN, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return taskARN
}

func extractClusterName(clusterARN string) string {
	// arn:aws:ecs:region:account:cluster/name
	parts := strings.Split(clusterARN, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return clusterARN
}
