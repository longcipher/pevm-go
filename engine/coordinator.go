package engine

import (
	"context"
	"time"

	"github.com/longcipher/pevm-go/access"
	"github.com/longcipher/pevm-go/common"
)

// Coordinator wires the planner and scheduler to reduce conflicts before execution.
type Coordinator struct {
	planner   *access.Planner
	scheduler SchedulerFacade
}

// SchedulerFacade abstracts the subset of scheduler used by the coordinator.
type SchedulerFacade interface {
	AddTasks(tasks []common.Task) error
}

// NewCoordinator creates a new Coordinator.
func NewCoordinator(planner *access.Planner, sched SchedulerFacade) *Coordinator {
	return &Coordinator{planner: planner, scheduler: sched}
}

// Prepare plans tasks (via BALs or oracle) and submit wrapped tasks to scheduler.
func (c *Coordinator) Prepare(ctx context.Context, tasks []common.Task) (partitions [][]common.TaskID, err error) {
	parts, wrapped := c.planner.Plan(tasks)
	// Submit wrapped tasks to scheduler to encode dependencies
	if err := c.scheduler.AddTasks(wrapped); err != nil {
		return nil, err
	}
	_ = ctx // reserved for timeouts/cancellation if needed later
	return parts, nil
}

// WithTimeout helper to bound preparation time.
func (c *Coordinator) WithTimeout(parent context.Context, d time.Duration, tasks []common.Task) ([][]common.TaskID, error) {
	ctx, cancel := context.WithTimeout(parent, d)
	defer cancel()
	return c.Prepare(ctx, tasks)
}
