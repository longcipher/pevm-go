// Package scheduler provides queue implementations for the PEVM scheduler.
package scheduler

import (
	"container/heap"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/longcipher/pevm-go/common"
)

// ChaseLevQueue implements a lock-free work-stealing queue using the Chase-Lev algorithm.
type ChaseLevQueue struct {
	buffer  unsafe.Pointer // *[]common.Task
	top     int64          // Atomic top index (for stealing)
	bottom  int64          // Atomic bottom index (for local operations)
	mask    int64          // Size mask for circular buffer
	maxSize int
}

// NewChaseLevQueue creates a new Chase-Lev work-stealing queue.
func NewChaseLevQueue(size int) *ChaseLevQueue {
	// Ensure size is a power of 2
	if size&(size-1) != 0 {
		size = int(nextPowerOfTwo(uint64(size)))
	}

	buffer := make([]common.Task, size)
	q := &ChaseLevQueue{
		buffer:  unsafe.Pointer(&buffer),
		mask:    int64(size - 1),
		maxSize: size,
	}
	return q
}

// PushBottom adds a task to the bottom of the queue (local end).
func (q *ChaseLevQueue) PushBottom(task common.Task) bool {
	bottom := atomic.LoadInt64(&q.bottom)
	top := atomic.LoadInt64(&q.top)

	// Check if queue is full
	if bottom-top >= int64(q.maxSize) {
		return false
	}

	// Get current buffer
	bufferPtr := atomic.LoadPointer(&q.buffer)
	buffer := *(*[]common.Task)(bufferPtr)

	// Store task at bottom
	buffer[bottom&q.mask] = task

	// Increment bottom
	atomic.StoreInt64(&q.bottom, bottom+1)
	return true
}

// PopBottom removes a task from the bottom of the queue (local end).
func (q *ChaseLevQueue) PopBottom() (common.Task, bool) {
	bottom := atomic.AddInt64(&q.bottom, -1)
	bufferPtr := atomic.LoadPointer(&q.buffer)
	buffer := *(*[]common.Task)(bufferPtr)

	top := atomic.LoadInt64(&q.top)

	if bottom < top {
		// Queue is empty
		atomic.StoreInt64(&q.bottom, top)
		return nil, false
	}

	task := buffer[bottom&q.mask]

	if bottom == top {
		// Last element - use CAS to ensure atomicity with steal operations
		if !atomic.CompareAndSwapInt64(&q.top, top, top+1) {
			// Steal occurred, queue is empty
			atomic.StoreInt64(&q.bottom, top+1)
			return nil, false
		}
		atomic.StoreInt64(&q.bottom, top+1)
	}

	return task, true
}

// PopTop removes a task from the top of the queue (stealing end).
func (q *ChaseLevQueue) PopTop() (common.Task, bool) {
	top := atomic.LoadInt64(&q.top)
	bottom := atomic.LoadInt64(&q.bottom)

	if top >= bottom {
		// Queue is empty
		return nil, false
	}

	bufferPtr := atomic.LoadPointer(&q.buffer)
	buffer := *(*[]common.Task)(bufferPtr)
	task := buffer[top&q.mask]

	// Use CAS to ensure we successfully steal the task
	if !atomic.CompareAndSwapInt64(&q.top, top, top+1) {
		// Another thread beat us to it
		return nil, false
	}

	return task, true
}

// Size returns the current size of the queue.
func (q *ChaseLevQueue) Size() int {
	bottom := atomic.LoadInt64(&q.bottom)
	top := atomic.LoadInt64(&q.top)
	size := bottom - top
	if size < 0 {
		return 0
	}
	return int(size)
}

// IsEmpty returns true if the queue is empty.
func (q *ChaseLevQueue) IsEmpty() bool {
	return q.Size() == 0
}

// AdaptivePriorityQueue implements a priority queue that adapts to access patterns.
type AdaptivePriorityQueue struct {
	heap     *taskHeap
	mutex    sync.Mutex
	maxSize  int
	hotTasks map[common.TaskID]int // Tracks frequently accessed tasks
	hotMutex sync.RWMutex
}

// taskHeapItem represents an item in the priority heap.
type taskHeapItem struct {
	task     common.Task
	priority float64
	index    int
}

// taskHeap implements heap.Interface for task priority queue.
type taskHeap []*taskHeapItem

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	// Higher priority values come first
	return h[i].priority > h[j].priority
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *taskHeap) Push(x interface{}) {
	item := x.(*taskHeapItem)
	item.index = len(*h)
	*h = append(*h, item)
}

func (h *taskHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[0 : n-1]
	return item
}

// NewAdaptivePriorityQueue creates a new adaptive priority queue.
func NewAdaptivePriorityQueue(maxSize int) *AdaptivePriorityQueue {
	pq := &AdaptivePriorityQueue{
		heap:     &taskHeap{},
		maxSize:  maxSize,
		hotTasks: make(map[common.TaskID]int),
	}
	heap.Init(pq.heap)
	return pq
}

// Push adds a task to the queue with the given priority.
func (pq *AdaptivePriorityQueue) Push(task common.Task, priority float64) bool {
	pq.mutex.Lock()
	defer pq.mutex.Unlock()

	if len(*pq.heap) >= pq.maxSize {
		return false
	}

	// Adapt priority based on historical access patterns
	adaptedPriority := pq.adaptPriority(task, priority)

	item := &taskHeapItem{
		task:     task,
		priority: adaptedPriority,
	}

	heap.Push(pq.heap, item)
	return true
}

// Pop removes the highest priority task from the queue.
func (pq *AdaptivePriorityQueue) Pop() (common.Task, bool) {
	pq.mutex.Lock()
	defer pq.mutex.Unlock()

	if len(*pq.heap) == 0 {
		return nil, false
	}

	item := heap.Pop(pq.heap).(*taskHeapItem)

	// Update hot task tracking
	pq.updateHotTask(item.task.ID())

	return item.task, true
}

// Size returns the current size of the queue.
func (pq *AdaptivePriorityQueue) Size() int {
	pq.mutex.Lock()
	defer pq.mutex.Unlock()
	return len(*pq.heap)
}

// IsEmpty returns true if the queue is empty.
func (pq *AdaptivePriorityQueue) IsEmpty() bool {
	return pq.Size() == 0
}

// adaptPriority adapts the priority based on historical access patterns.
func (pq *AdaptivePriorityQueue) adaptPriority(task common.Task, basePriority float64) float64 {
	pq.hotMutex.RLock()
	hotCount, isHot := pq.hotTasks[task.ID()]
	pq.hotMutex.RUnlock()

	if isHot && hotCount > 5 {
		// Boost priority for frequently accessed tasks
		return basePriority * 1.5
	}

	return basePriority
}

// updateHotTask updates the hot task tracking.
func (pq *AdaptivePriorityQueue) updateHotTask(taskID common.TaskID) {
	pq.hotMutex.Lock()
	pq.hotTasks[taskID]++
	pq.hotMutex.Unlock()
}

// DistributedQueue implements a queue that distributes tasks across multiple underlying queues.
type DistributedQueue struct {
	queues    []PriorityQueue
	nextQueue int64 // Atomic counter for round-robin distribution
	numQueues int
	strategy  DistributionStrategy
}

// DistributionStrategy determines how tasks are distributed across queues.
type DistributionStrategy int

const (
	StrategyRoundRobin DistributionStrategy = iota
	StrategyLoadBased
	StrategyHash
)

// NewDistributedQueue creates a new distributed queue.
func NewDistributedQueue(numQueues int, queueSize int, strategy DistributionStrategy) *DistributedQueue {
	queues := make([]PriorityQueue, numQueues)
	for i := 0; i < numQueues; i++ {
		queues[i] = NewAdaptivePriorityQueue(queueSize)
	}

	return &DistributedQueue{
		queues:    queues,
		numQueues: numQueues,
		strategy:  strategy,
	}
}

// Push adds a task to one of the underlying queues based on the distribution strategy.
func (dq *DistributedQueue) Push(task common.Task, priority float64) bool {
	queueIndex := dq.selectQueue(task)
	return dq.queues[queueIndex].Push(task, priority)
}

// Pop removes a task from the queue with the highest priority task.
func (dq *DistributedQueue) Pop() (common.Task, bool) {
	// Try each queue to find the highest priority task
	var bestTask common.Task
	var bestQueue int = -1
	var bestPriority float64 = -1

	for i, queue := range dq.queues {
		if !queue.IsEmpty() {
			// Peek at the top task (this is a simplified implementation)
			// In a real implementation, we'd want a more efficient way to peek
			if task, ok := queue.Pop(); ok {
				// For now, we'll use the task's estimated gas as a proxy for priority
				priority := float64(task.EstimatedGas())
				if priority > bestPriority {
					if bestQueue != -1 {
						// Put the previous best task back
						dq.queues[bestQueue].Push(bestTask, bestPriority)
					}
					bestTask = task
					bestQueue = i
					bestPriority = priority
				} else {
					// Put this task back
					queue.Push(task, priority)
				}
			}
		}
	}

	if bestQueue != -1 {
		return bestTask, true
	}

	return nil, false
}

// Size returns the total size across all queues.
func (dq *DistributedQueue) Size() int {
	total := 0
	for _, queue := range dq.queues {
		total += queue.Size()
	}
	return total
}

// IsEmpty returns true if all queues are empty.
func (dq *DistributedQueue) IsEmpty() bool {
	for _, queue := range dq.queues {
		if !queue.IsEmpty() {
			return false
		}
	}
	return true
}

// selectQueue selects which queue to use based on the distribution strategy.
func (dq *DistributedQueue) selectQueue(task common.Task) int {
	switch dq.strategy {
	case StrategyRoundRobin:
		return int(atomic.AddInt64(&dq.nextQueue, 1) % int64(dq.numQueues))
	case StrategyLoadBased:
		return dq.selectLeastLoadedQueue()
	case StrategyHash:
		return int(task.Hash().Big().Uint64() % uint64(dq.numQueues))
	default:
		return 0
	}
}

// selectLeastLoadedQueue finds the queue with the smallest size.
func (dq *DistributedQueue) selectLeastLoadedQueue() int {
	minSize := int(^uint(0) >> 1) // Max int
	selectedQueue := 0

	for i, queue := range dq.queues {
		size := queue.Size()
		if size < minSize {
			minSize = size
			selectedQueue = i
		}
	}

	return selectedQueue
}

// nextPowerOfTwo returns the next power of two greater than or equal to n.
func nextPowerOfTwo(n uint64) uint64 {
	if n == 0 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	n++
	return n
}
