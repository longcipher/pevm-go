package engine

import (
	"context"
	"testing"
	"time"

	gethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/longcipher/pevm-go/access"
	pevm "github.com/longcipher/pevm-go/common"
)

type fakeSched struct{ added int }

func (f *fakeSched) AddTasks(tasks []pevm.Task) error { f.added += len(tasks); return nil }

type tMock struct{ pevm.Task }

func TestCoordinator_Prepare(t *testing.T) {
	// Empty oracle -> no extra deps, still adds tasks
	oracle := access.NewStaticOracle(access.List{}, pevm.KeyTypeStorage)
	planner := access.NewPlanner(oracle)
	sched := &fakeSched{}
	coord := NewCoordinator(planner, sched)

	tasks := []pevm.Task{}
	parts, err := coord.Prepare(context.Background(), tasks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 0 || sched.added != 0 {
		t.Fatalf("expected no parts and no tasks added, got parts=%d added=%d", len(parts), sched.added)
	}

	// With simple tasks
	// provide two minimal tasks via planner test mock
	m1 := &access_mockTask{IDVal: 1}
	m2 := &access_mockTask{IDVal: 2}
	tasks = []pevm.Task{m1, m2}
	parts, err = coord.WithTimeout(context.Background(), time.Second, tasks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) < 1 {
		t.Fatalf("expected at least one partition")
	}
	if sched.added == 0 {
		t.Fatalf("expected tasks to be added to scheduler")
	}
}

// access_mockTask mirrors the mockTask used in access tests without import cycles.
type access_mockTask struct{ IDVal pevm.TaskID }

func (m *access_mockTask) ID() pevm.TaskID                  { return m.IDVal }
func (m *access_mockTask) Dependencies() []pevm.TaskID      { return nil }
func (m *access_mockTask) EstimatedGas() uint64             { return 1 }
func (m *access_mockTask) EstimatedDuration() time.Duration { return time.Millisecond }
func (m *access_mockTask) Sender() gethcommon.Address       { return gethcommon.Address{} }
func (m *access_mockTask) Hash() gethcommon.Hash            { return gethcommon.Hash{} }
