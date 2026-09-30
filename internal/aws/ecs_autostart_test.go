package aws

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// fakeECSClient is a test double for ecsAPI. Only the methods a test cares
// about need a non-nil func field — calling an unset one panics with a
// clear message, so an untested code path fails loudly instead of silently
// returning a zero value.
type fakeECSClient struct {
	describeServicesFn func(context.Context, *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error)
	runTaskFn          func(context.Context, *ecs.RunTaskInput) (*ecs.RunTaskOutput, error)
	describeTasksFn    func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error)
	stopTaskFn         func(context.Context, *ecs.StopTaskInput) (*ecs.StopTaskOutput, error)
	listClustersFn     func(context.Context, *ecs.ListClustersInput) (*ecs.ListClustersOutput, error)
	listServicesFn     func(context.Context, *ecs.ListServicesInput) (*ecs.ListServicesOutput, error)
	listTasksFn        func(context.Context, *ecs.ListTasksInput) (*ecs.ListTasksOutput, error)
}

func (f *fakeECSClient) DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if f.describeServicesFn == nil {
		panic("fakeECSClient: DescribeServices called but not stubbed")
	}
	return f.describeServicesFn(ctx, in)
}

func (f *fakeECSClient) RunTask(ctx context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	if f.runTaskFn == nil {
		panic("fakeECSClient: RunTask called but not stubbed")
	}
	return f.runTaskFn(ctx, in)
}

func (f *fakeECSClient) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	if f.describeTasksFn == nil {
		panic("fakeECSClient: DescribeTasks called but not stubbed")
	}
	return f.describeTasksFn(ctx, in)
}

func (f *fakeECSClient) StopTask(ctx context.Context, in *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	if f.stopTaskFn == nil {
		panic("fakeECSClient: StopTask called but not stubbed")
	}
	return f.stopTaskFn(ctx, in)
}

func (f *fakeECSClient) ListClusters(ctx context.Context, in *ecs.ListClustersInput, _ ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	if f.listClustersFn == nil {
		panic("fakeECSClient: ListClusters called but not stubbed")
	}
	return f.listClustersFn(ctx, in)
}

func (f *fakeECSClient) ListServices(ctx context.Context, in *ecs.ListServicesInput, _ ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	if f.listServicesFn == nil {
		panic("fakeECSClient: ListServices called but not stubbed")
	}
	return f.listServicesFn(ctx, in)
}

func (f *fakeECSClient) ListTasks(ctx context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	if f.listTasksFn == nil {
		panic("fakeECSClient: ListTasks called but not stubbed")
	}
	return f.listTasksFn(ctx, in)
}

// runTaskOK stubs RunTaskFromService's two calls (DescribeServices, RunTask)
// with a minimal successful task launch, returning taskARN.
func runTaskOK(taskARN string) *fakeECSClient {
	return &fakeECSClient{
		describeServicesFn: func(context.Context, *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error) {
			return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
				TaskDefinition: aws.String("arn:aws:ecs:us-east-1:123:task-definition/bastion:1"),
			}}}, nil
		},
		runTaskFn: func(context.Context, *ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
			return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String(taskARN)}}}, nil
		},
	}
}

func TestAutoStartECSServiceStopsOrphanedTaskOnFailure(t *testing.T) {
	const taskARN = "arn:aws:ecs:us-east-1:123:task/cluster/abc123"

	t.Run("task never becomes ready — cleanup succeeds, error says so", func(t *testing.T) {
		client := runTaskOK(taskARN)
		client.describeTasksFn = func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			// STOPPED is a terminal state — WaitForTaskReady fails fast with
			// no backoff/sleep, so this test runs instantly.
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				LastStatus:    aws.String("STOPPED"),
				StoppedReason: aws.String("essential container exited"),
			}}}, nil
		}
		var stoppedARN string
		client.stopTaskFn = func(_ context.Context, in *ecs.StopTaskInput) (*ecs.StopTaskOutput, error) {
			stoppedARN = aws.ToString(in.Task)
			return &ecs.StopTaskOutput{}, nil
		}

		d := &Discovery{ecs: client}
		_, err := d.autoStartECSService(context.Background(), "cluster", "service")

		if err == nil {
			t.Fatal("expected an error — the task never became ready")
		}
		if stoppedARN != taskARN {
			t.Errorf("StopTask called with %q, want %q — cleanup didn't target the task it started", stoppedARN, taskARN)
		}
		if !strings.Contains(err.Error(), "auto-stopped the task it started") {
			t.Errorf("error = %q, want it to mention the cleanup succeeded", err.Error())
		}
	})

	t.Run("task never becomes ready AND cleanup itself fails — both are reported", func(t *testing.T) {
		client := runTaskOK(taskARN)
		client.describeTasksFn = func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				LastStatus:    aws.String("STOPPED"),
				StoppedReason: aws.String("essential container exited"),
			}}}, nil
		}
		client.stopTaskFn = func(context.Context, *ecs.StopTaskInput) (*ecs.StopTaskOutput, error) {
			return nil, errors.New("access denied")
		}

		d := &Discovery{ecs: client}
		_, err := d.autoStartECSService(context.Background(), "cluster", "service")

		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "failed to stop the task it started") {
			t.Errorf("error = %q, want it to mention the cleanup ALSO failed, not just discard it", err.Error())
		}
		if !strings.Contains(err.Error(), "access denied") {
			t.Errorf("error = %q, want it to include StopTask's own error", err.Error())
		}
	})

	t.Run("task becomes ready — no cleanup, no error", func(t *testing.T) {
		client := runTaskOK(taskARN)
		client.describeTasksFn = func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String(taskARN),
				LastStatus: aws.String("RUNNING"),
				Containers: []ecstypes.Container{{
					RuntimeId: aws.String("runtime-1"),
					ManagedAgents: []ecstypes.ManagedAgent{{
						Name:       ecstypes.ManagedAgentNameExecuteCommandAgent,
						LastStatus: aws.String("RUNNING"),
					}},
				}},
			}}}, nil
		}
		client.stopTaskFn = func(context.Context, *ecs.StopTaskInput) (*ecs.StopTaskOutput, error) {
			t.Fatal("StopTask should not be called when the task became ready")
			return nil, nil
		}

		d := &Discovery{ecs: client}
		task, err := d.autoStartECSService(context.Background(), "cluster", "service")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if task.TaskARN != taskARN {
			t.Errorf("task.TaskARN = %q, want %q", task.TaskARN, taskARN)
		}
	})
}

func TestWaitForTaskReadyFailsFastOnStoppedExecAgent(t *testing.T) {
	const taskARN = "arn:aws:ecs:us-east-1:123:task/cluster/abc123"

	// The exec agent going STOPPED (commonly a read-only root fs) is
	// terminal — WaitForTaskReady should return immediately with a clear
	// reason rather than retrying until DefaultStartupTimeout, which would
	// make this test (and a real cold start hitting this) take minutes.
	client := &fakeECSClient{
		describeTasksFn: func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String(taskARN),
				LastStatus: aws.String("RUNNING"),
				Containers: []ecstypes.Container{{
					RuntimeId: aws.String("runtime-1"),
					ManagedAgents: []ecstypes.ManagedAgent{{
						Name:       ecstypes.ManagedAgentNameExecuteCommandAgent,
						LastStatus: aws.String("STOPPED"),
					}},
				}},
			}}}, nil
		},
	}

	d := &Discovery{ecs: client}
	_, err := d.WaitForTaskReady(context.Background(), "cluster", taskARN, "service", DefaultStartupTimeout, nil)

	if err == nil {
		t.Fatal("expected an error — the exec agent will never come back")
	}
	if !strings.Contains(err.Error(), "exec agent STOPPED") {
		t.Errorf("error = %q, want it to name the exec agent as the cause", err.Error())
	}
}
