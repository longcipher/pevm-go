package access

import (
	"time"

	gethcommon "github.com/ethereum/go-ethereum/common"
	pevm "github.com/longcipher/pevm-go/common"
)

// Planner builds conflict-aware execution plans using an AccessOracle (e.g., BAL-informed).
type Planner struct {
	oracle AccessOracle
}

// NewPlanner creates a new Planner.
func NewPlanner(oracle AccessOracle) *Planner {
	return &Planner{oracle: oracle}
}

// Plan computes conflict-free partitions and optional extra dependencies for tasks.
// It returns:
// - partitions: a list of batches; tasks within the same batch have disjoint access keys
// - wrapped: tasks wrapped to inject extra dependencies (if any)
func (p *Planner) Plan(tasks []pevm.Task) (partitions [][]pevm.TaskID, wrapped []pevm.Task) {
	// Build key -> tasks map and a conflict graph
	keyOwners := make(map[uint64][]pevm.TaskID)
	for _, t := range tasks {
		keys := p.oracle.KeysFor(t)
		for _, k := range keys {
			keyOwners[k.Hash()] = append(keyOwners[k.Hash()], t.ID())
		}
	}

	// Build conflict adjacency: tasks that share any key conflict
	adj := make(map[pevm.TaskID]map[pevm.TaskID]struct{})
	for _, owners := range keyOwners {
		for i := 0; i < len(owners); i++ {
			for j := i + 1; j < len(owners); j++ {
				a, b := owners[i], owners[j]
				if adj[a] == nil {
					adj[a] = make(map[pevm.TaskID]struct{})
				}
				if adj[b] == nil {
					adj[b] = make(map[pevm.TaskID]struct{})
				}
				adj[a][b] = struct{}{}
				adj[b][a] = struct{}{}
			}
		}
	}

	// Greedy graph coloring -> partitions
	colorOf := make(map[pevm.TaskID]int)
	for _, t := range tasks {
		used := make(map[int]bool)
		for n := range adj[t.ID()] {
			if c, ok := colorOf[n]; ok {
				used[c] = true
			}
		}
		// pick first free color
		c := 0
		for used[c] {
			c++
		}
		colorOf[t.ID()] = c
	}

	// Build partitions by color index
	maxColor := -1
	for _, c := range colorOf {
		if c > maxColor {
			maxColor = c
		}
	}
	parts := make([][]pevm.TaskID, maxColor+1)
	for _, t := range tasks {
		c := colorOf[t.ID()]
		parts[c] = append(parts[c], t.ID())
	}

	// Wrap tasks to add dependencies: ensure tasks that share keys across partitions
	// depend on the earlier-colored counterpart to reduce aborts
	partIndex := make(map[pevm.TaskID]int)
	for i, ids := range parts {
		for _, id := range ids {
			partIndex[id] = i
		}
	}

	// Map original ID -> original task
	byID := make(map[pevm.TaskID]pevm.Task)
	for _, t := range tasks {
		byID[t.ID()] = t
	}

	wrapped = make([]pevm.Task, 0, len(tasks))
	for _, t := range tasks {
		extra := make([]pevm.TaskID, 0)
		for n := range adj[t.ID()] {
			if partIndex[n] < partIndex[t.ID()] {
				extra = append(extra, n)
			}
		}
		if len(extra) == 0 {
			wrapped = append(wrapped, t)
			continue
		}
		wrapped = append(wrapped, &wrappedTask{inner: t, extraDeps: extra})
	}

	return parts, wrapped
}

// wrappedTask injects extra dependencies while delegating to the inner task.
type wrappedTask struct {
	inner     pevm.Task
	extraDeps []pevm.TaskID
}

func (w *wrappedTask) ID() pevm.TaskID                  { return w.inner.ID() }
func (w *wrappedTask) EstimatedGas() uint64             { return w.inner.EstimatedGas() }
func (w *wrappedTask) EstimatedDuration() time.Duration { return w.inner.EstimatedDuration() }
func (w *wrappedTask) Sender() gethcommon.Address       { return w.inner.Sender() }
func (w *wrappedTask) Hash() gethcommon.Hash            { return w.inner.Hash() }
func (w *wrappedTask) Dependencies() []pevm.TaskID {
	base := w.inner.Dependencies()
	if len(w.extraDeps) == 0 {
		return base
	}
	out := make([]pevm.TaskID, 0, len(base)+len(w.extraDeps))
	out = append(out, base...)
	out = append(out, w.extraDeps...)
	return out
}
