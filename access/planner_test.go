package access

import (
	"testing"
	"time"

	gethcommon "github.com/ethereum/go-ethereum/common"
	pevm "github.com/longcipher/pevm-go/common"
)

type mockTask struct {
	id   pevm.TaskID
	deps []pevm.TaskID
}

func (m *mockTask) ID() pevm.TaskID                  { return m.id }
func (m *mockTask) Dependencies() []pevm.TaskID      { return m.deps }
func (m *mockTask) EstimatedGas() uint64             { return 21000 }
func (m *mockTask) EstimatedDuration() time.Duration { return time.Millisecond }
func (m *mockTask) Sender() gethcommon.Address       { return gethcommon.Address{} }
func (m *mockTask) Hash() gethcommon.Hash            { return gethcommon.Hash{} }

func TestPlanner_PartitionsAndDeps(t *testing.T) {
	// Create three tasks: t0 and t1 conflict on same key; t2 independent
	t0 := &mockTask{id: 0}
	t1 := &mockTask{id: 1}
	t2 := &mockTask{id: 2}
	tasks := []pevm.Task{t0, t1, t2}

	addr := gethcommon.HexToAddress("0x0000000000000000000000000000000000000001")
	slot := gethcommon.HexToHash("0x01")

	bal := List{
		0: {{Address: addr, StorageKeys: []gethcommon.Hash{slot}}},
		1: {{Address: addr, StorageKeys: []gethcommon.Hash{slot}}},
		2: {{Address: addr, StorageKeys: []gethcommon.Hash{}}},
	}
	oracle := NewStaticOracle(bal, pevm.KeyTypeStorage)
	planner := NewPlanner(oracle)

	parts, wrapped := planner.Plan(tasks)
	if len(wrapped) != 3 {
		t.Fatalf("expected 3 wrapped tasks, got %d", len(wrapped))
	}

	// Expect at least 2 partitions due to conflict between t0 and t1
	if len(parts) < 2 {
		t.Fatalf("expected >=2 partitions, got %d", len(parts))
	}

	// One of t0/t1 should depend on the other after wrapping (in later partition)
	// Find which was assigned to later partition
	// We infer via Dependencies() length > 0 on exactly one of them
	depCounts := 0
	for _, w := range wrapped {
		if w.ID() == 0 || w.ID() == 1 {
			if len(w.Dependencies()) > 0 {
				depCounts++
			}
		}
	}
	if depCounts != 1 {
		t.Fatalf("expected exactly one of t0/t1 to gain extra deps, got %d", depCounts)
	}
}
