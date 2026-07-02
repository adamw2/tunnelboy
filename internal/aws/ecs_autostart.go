package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// DefaultStartupTimeout is the deadline for an auto-started ECS task to reach
// a fully exec-ready state. Fargate cold starts can take ~60-90s plus agent
// initialization, so we allow generous headroom.
const DefaultStartupTimeout = 3 * time.Minute

// ProgressFunc receives elapsed time and a short status string during waits.
// Used to surface "Task PROVISIONING (12s)..." style feedback in the CLI.
type ProgressFunc func(elapsed time.Duration, status string)

// RunTaskFromService launches a single ephemeral task using the discovered
// service's blueprint (task definition + networking + launch settings) with
// ECS Exec enabled. It does NOT touch the service's desired count. Returns the
// new task ARN.
//
// Exec is enabled per-task here (not inherited from the service) so the agent
// reliably starts — matching a manual `run-task --enable-execute-command`.
func (d *Discovery) RunTaskFromService(ctx context.Context, cluster, service string) (string, error) {
	client := ecs.NewFromConfig(d.cfg)

	desc, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []string{service},
	})
	if err != nil {
		return "", fmt.Errorf("describe service %s/%s: %w", cluster, service, err)
	}
	if len(desc.Services) == 0 {
		return "", fmt.Errorf("service %s/%s not found", cluster, service)
	}
	svc := desc.Services[0]

	input := &ecs.RunTaskInput{
		Cluster:              aws.String(cluster),
		TaskDefinition:       svc.TaskDefinition,
		Count:                aws.Int32(1),
		EnableExecuteCommand: true,
		StartedBy:            aws.String("tunnelboy"),
		NetworkConfiguration: svc.NetworkConfiguration,
	}
	// LaunchType and CapacityProviderStrategy are mutually exclusive; mirror
	// whichever the service uses.
	if len(svc.CapacityProviderStrategy) > 0 {
		input.CapacityProviderStrategy = svc.CapacityProviderStrategy
	} else if svc.LaunchType != "" {
		input.LaunchType = svc.LaunchType
	}
	if svc.PlatformVersion != nil {
		input.PlatformVersion = svc.PlatformVersion
	}

	out, err := client.RunTask(ctx, input)
	if err != nil {
		return "", fmt.Errorf("run task from %s/%s: %w", cluster, service, err)
	}
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		return "", fmt.Errorf("run task from %s/%s failed: %s (%s)", cluster, service,
			aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 {
		return "", fmt.Errorf("run task from %s/%s returned no task", cluster, service)
	}
	return aws.ToString(out.Tasks[0].TaskArn), nil
}

// WaitForTaskReady polls a specific task ARN until it is RUNNING with the SSM
// exec agent ready, then returns it. Fails fast on terminal states (task
// STOPPED, or exec agent STOPPED) instead of waiting out the timeout.
func (d *Discovery) WaitForTaskReady(ctx context.Context, cluster, taskARN, service string, timeout time.Duration, progress ProgressFunc) (*ECSTask, error) {
	if timeout <= 0 {
		timeout = DefaultStartupTimeout
	}
	client := ecs.NewFromConfig(d.cfg)
	deadline := time.Now().Add(timeout)
	start := time.Now()

	backoff := time.Second
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for task %s", timeout, extractTaskID(taskARN))
		}

		desc, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   []string{taskARN},
		})
		if err != nil {
			return nil, fmt.Errorf("describe task %s: %w", extractTaskID(taskARN), err)
		}
		if len(desc.Tasks) == 0 {
			emit(progress, start, "PENDING")
			if err := sleepCtx(ctx, backoff); err != nil {
				return nil, err
			}
			backoff = nextBackoff(backoff)
			continue
		}

		t := desc.Tasks[0]
		status := aws.ToString(t.LastStatus)
		if status == "STOPPED" {
			return nil, fmt.Errorf("task %s stopped before becoming ready: %s",
				extractTaskID(taskARN), aws.ToString(t.StoppedReason))
		}
		if status == "RUNNING" {
			task, ready, retryable, reason := buildReadyTask(t, cluster, service)
			if ready {
				emit(progress, start, "READY")
				return task, nil
			}
			if !retryable {
				return nil, fmt.Errorf("task %s cannot become ready: %s", extractTaskID(taskARN), reason)
			}
			status = reason
		}

		emit(progress, start, status)
		if err := sleepCtx(ctx, backoff); err != nil {
			return nil, err
		}
		backoff = nextBackoff(backoff)
	}
}

// StopTask stops a running task (used to tear down tunnelboy-started ephemeral
// tasks). Best-effort: callers typically log rather than fail on error.
func (d *Discovery) StopTask(ctx context.Context, cluster, taskARN, reason string) error {
	client := ecs.NewFromConfig(d.cfg)
	_, err := client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(cluster),
		Task:    aws.String(taskARN),
		Reason:  aws.String(reason),
	})
	if err != nil {
		return fmt.Errorf("stop task %s: %w", extractTaskID(taskARN), err)
	}
	return nil
}

// buildReadyTask inspects a DescribeTasks result and returns a populated
// ECSTask, a readiness flag, a retryable flag, and a status string. When
// ready is false and retryable is false, the task can never become ready
// (e.g. exec agent STOPPED) and the caller should fail fast rather than poll.
func buildReadyTask(t ecstypes.Task, cluster, service string) (task *ECSTask, ready, retryable bool, status string) {
	task = &ECSTask{
		TaskARN:        aws.ToString(t.TaskArn),
		TaskID:         extractTaskID(aws.ToString(t.TaskArn)),
		ClusterARN:     cluster,
		ClusterName:    extractClusterName(cluster),
		ServiceName:    service,
		TaskDefinition: extractTaskFamily(aws.ToString(t.TaskDefinitionArn)),
		Status:         aws.ToString(t.LastStatus),
	}

	for _, attachment := range t.Attachments {
		if aws.ToString(attachment.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, detail := range attachment.Details {
			if aws.ToString(detail.Name) == "privateIPv4Address" {
				task.PrivateIP = aws.ToString(detail.Value)
			}
		}
	}

	if len(t.Containers) == 0 {
		return task, false, true, "no containers"
	}
	runtimeID := aws.ToString(t.Containers[0].RuntimeId)
	if runtimeID == "" {
		return task, false, true, "awaiting runtime id"
	}
	task.RuntimeID = runtimeID
	task.SSMTarget = fmt.Sprintf("ecs:%s_%s_%s", task.ClusterName, task.TaskID, runtimeID)

	// Exec agent must be RUNNING — otherwise SSM start-session silently fails.
	// STOPPED is terminal (commonly a read-only root fs blocking the SSM agent);
	// don't burn the whole timeout waiting for it to recover.
	for _, c := range t.Containers {
		for _, agent := range c.ManagedAgents {
			if agent.Name != ecstypes.ManagedAgentNameExecuteCommandAgent {
				continue
			}
			switch aws.ToString(agent.LastStatus) {
			case "RUNNING":
				return task, true, false, "READY"
			case "STOPPED":
				return task, false, false, "exec agent STOPPED (check executeCommand config / read-only root fs)"
			default:
				return task, false, true, fmt.Sprintf("exec agent %s", aws.ToString(agent.LastStatus))
			}
		}
	}
	return task, false, true, "awaiting exec agent"
}

// ServiceRef identifies an ECS service by its cluster and service name.
type ServiceRef struct {
	Cluster string
	Service string
}

// FindECSServicesByPattern lists services across all clusters and returns
// those whose service name or current task-definition family matches any of
// the supplied patterns. Used by pattern-based auto-start to figure out which
// service to scale when no tasks are running.
func (d *Discovery) FindECSServicesByPattern(ctx context.Context, patterns []string) ([]ServiceRef, error) {
	client := ecs.NewFromConfig(d.cfg)

	clusters, err := client.ListClusters(ctx, &ecs.ListClustersInput{})
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}

	seen := make(map[string]bool)
	var matches []ServiceRef
	for _, clusterARN := range clusters.ClusterArns {
		svcARNs, err := client.ListServices(ctx, &ecs.ListServicesInput{Cluster: aws.String(clusterARN)})
		if err != nil || len(svcARNs.ServiceArns) == 0 {
			continue
		}
		// DescribeServices accepts up to 10 services at a time.
		for i := 0; i < len(svcARNs.ServiceArns); i += 10 {
			end := i + 10
			if end > len(svcARNs.ServiceArns) {
				end = len(svcARNs.ServiceArns)
			}
			desc, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
				Cluster:  aws.String(clusterARN),
				Services: svcARNs.ServiceArns[i:end],
			})
			if err != nil {
				continue
			}
			for _, svc := range desc.Services {
				name := aws.ToString(svc.ServiceName)
				family := extractTaskFamily(aws.ToString(svc.TaskDefinition))
				if !matchesAny(patterns, name) && !matchesAny(patterns, family) {
					continue
				}
				key := clusterARN + "|" + name
				if seen[key] {
					continue
				}
				seen[key] = true
				matches = append(matches, ServiceRef{Cluster: clusterARN, Service: name})
			}
		}
	}
	return matches, nil
}

func matchesAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if matchPattern(s, p) {
			return true
		}
	}
	return false
}

// autoStartECSService launches an ephemeral task from the service blueprint and
// waits for it to become exec-ready. If it never becomes ready, the started
// task is stopped so we don't leak a billing Fargate task.
func (d *Discovery) autoStartECSService(ctx context.Context, cluster, service string) (*ECSTask, error) {
	if d.progress != nil {
		d.progress(0, fmt.Sprintf("starting %s/%s", extractClusterName(cluster), service))
	}
	taskARN, err := d.RunTaskFromService(ctx, cluster, service)
	if err != nil {
		return nil, err
	}
	task, err := d.WaitForTaskReady(ctx, cluster, taskARN, service, DefaultStartupTimeout, d.progress)
	if err != nil {
		// Best-effort cleanup of the task we just started; use a fresh context
		// in case the caller's was cancelled.
		_ = d.StopTask(context.Background(), cluster, taskARN, "tunnelboy: task never became ready")
		return nil, err
	}
	return task, nil
}

func emit(p ProgressFunc, start time.Time, status string) {
	if p == nil {
		return
	}
	p(time.Since(start).Round(time.Second), status)
}

// nextBackoff caps at 2s: DescribeTasks is free and low-rate, and a longer
// interval just adds dead time between the task becoming exec-ready and us
// noticing (up to a full interval per cold start).
func nextBackoff(cur time.Duration) time.Duration {
	next := cur + time.Second
	if next > 2*time.Second {
		return 2 * time.Second
	}
	return next
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), errors.New("wait cancelled"))
	case <-t.C:
		return nil
	}
}
