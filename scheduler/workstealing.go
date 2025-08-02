// Package scheduler provides intelligent task scheduling for the PEVM engine.
package scheduler

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/longcipher/pevm-go/common"
)

// Scheduler defines the interface for task scheduling operations.
type Scheduler interface {
	// AddTasks adds tasks to the scheduler for execution.
	AddTasks(tasks []common.Task) error

	// NextTask returns the next task for the specified worker to execute.
	NextTask(workerID int) (common.Task, bool)

	// CompleteTask marks a task as completed and handles dependencies.
	CompleteTask(taskID common.TaskID, result common.ExecutionResult) error

	// AbortTask marks a task as aborted and schedules it for re-execution.
	AbortTask(taskID common.TaskID, dependency common.TaskID) error

	// GetStatus returns the current scheduling status.
	GetStatus() SchedulerStatus

	// GetMetrics returns scheduling metrics.
	GetMetrics() SchedulerMetrics

	// Close shuts down the scheduler.
	Close() error
}

// SchedulerStatus represents the current state of the scheduler.
type SchedulerStatus struct {
	TotalTasks     int                           // Total number of tasks
	PendingTasks   int                           // Tasks waiting to be executed
	RunningTasks   int                           // Tasks currently being executed
	CompletedTasks int                           // Tasks that have completed
	FailedTasks    int                           // Tasks that have failed
	WorkerStatus   map[int]WorkerSchedulerStatus // Status of each worker
}

// WorkerSchedulerStatus represents the status of a worker from scheduler's perspective.
type WorkerSchedulerStatus struct {
	CurrentTask    common.TaskID // Currently assigned task (-1 if idle)
	TasksAssigned  int64         // Total tasks assigned to this worker
	TasksCompleted int64         // Tasks completed by this worker
	LastActivity   time.Time     // Last time this worker was active
}

// SchedulerMetrics contains performance metrics for the scheduler.
type SchedulerMetrics struct {
	SchedulingLatency        time.Duration // Average time to schedule a task
	LoadBalanceEfficiency    float64       // How evenly work is distributed (0-1)
	DependencyResolutionTime time.Duration // Time spent resolving dependencies
	WorkStealingEvents       int64         // Number of work stealing events
	ConflictDetectionTime    time.Duration // Time spent detecting conflicts
	TaskQueueDepth           int           // Current depth of task queue
	AvgTaskExecutionTime     time.Duration // Average task execution time
}

// WorkStealingScheduler implements a work-stealing scheduler with dependency awareness.
type WorkStealingScheduler struct {
	// Configuration
	config Config

	// Task management
	allTasks     map[common.TaskID]common.Task
	taskStatus   map[common.TaskID]TaskStatus
	dependencies map[common.TaskID][]common.TaskID // Tasks this task depends on
	dependents   map[common.TaskID][]common.TaskID // Tasks that depend on this task
	taskMutex    sync.RWMutex

	// Worker queues - each worker has its own double-ended queue
	workerQueues []WorkStealingQueue
	workerStatus []WorkerSchedulerStatus
	workerMutex  sync.RWMutex

	// Global ready queue for tasks with no dependencies
	readyQueue PriorityQueue
	readyMutex sync.Mutex

	// Statistics and metrics
	metrics           SchedulerMetrics
	startTime         time.Time
	lastMetricsUpdate time.Time

	// Shutdown coordination
	shutdown int32
	wg       sync.WaitGroup
}

// TaskStatus represents the current status of a task.
type TaskStatus uint8

const (
	TaskStatusPending   TaskStatus = iota // Task is waiting for dependencies
	TaskStatusReady                       // Task is ready to be executed
	TaskStatusRunning                     // Task is currently being executed
	TaskStatusCompleted                   // Task has completed successfully
	TaskStatusAborted                     // Task was aborted due to conflicts
	TaskStatusFailed                      // Task failed with an error
)

// String returns a string representation of the task status.
func (ts TaskStatus) String() string {
	switch ts {
	case TaskStatusPending:
		return "pending"
	case TaskStatusReady:
		return "ready"
	case TaskStatusRunning:
		return "running"
	case TaskStatusCompleted:
		return "completed"
	case TaskStatusAborted:
		return "aborted"
	case TaskStatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// WorkStealingQueue represents a double-ended queue for work stealing.
type WorkStealingQueue interface {
	// PushBottom adds a task to the bottom of the queue (local end).
	PushBottom(task common.Task) bool

	// PopBottom removes a task from the bottom of the queue (local end).
	PopBottom() (common.Task, bool)

	// PopTop removes a task from the top of the queue (stealing end).
	PopTop() (common.Task, bool)

	// Size returns the current size of the queue.
	Size() int

	// IsEmpty returns true if the queue is empty.
	IsEmpty() bool
}

// PriorityQueue represents a priority queue for tasks.
type PriorityQueue interface {
	// Push adds a task to the queue with the given priority.
	Push(task common.Task, priority float64) bool

	// Pop removes the highest priority task from the queue.
	Pop() (common.Task, bool)

	// Size returns the current size of the queue.
	Size() int

	// IsEmpty returns true if the queue is empty.
	IsEmpty() bool
}

// Config contains configuration options for the scheduler.
type Config struct {
	NumWorkers         int           // Number of worker threads
	QueueSize          int           // Size of each worker queue
	StealingEnabled    bool          // Whether work stealing is enabled
	LoadBalancing      bool          // Whether load balancing is enabled
	DependencyAnalysis bool          // Whether to perform dependency analysis
	AdaptiveScheduling bool          // Whether to use adaptive scheduling
	MetricsInterval    time.Duration // How often to update metrics
	TaskTimeout        time.Duration // Maximum time a task can run
}

// NewWorkStealingScheduler creates a new work-stealing scheduler.
func NewWorkStealingScheduler(config Config) *WorkStealingScheduler {
	if config.NumWorkers <= 0 {
		config.NumWorkers = DefaultNumWorkers
	}
	if config.QueueSize <= 0 {
		config.QueueSize = DefaultQueueSize
	}
	if config.MetricsInterval <= 0 {
		config.MetricsInterval = DefaultMetricsInterval
	}

	scheduler := &WorkStealingScheduler{
		config:       config,
		allTasks:     make(map[common.TaskID]common.Task),
		taskStatus:   make(map[common.TaskID]TaskStatus),
		dependencies: make(map[common.TaskID][]common.TaskID),
		dependents:   make(map[common.TaskID][]common.TaskID),
		workerQueues: make([]WorkStealingQueue, config.NumWorkers),
		workerStatus: make([]WorkerSchedulerStatus, config.NumWorkers),
		readyQueue:   NewAdaptivePriorityQueue(config.QueueSize * config.NumWorkers),
		startTime:    time.Now(),
	}

	// Initialize worker queues
	for i := 0; i < config.NumWorkers; i++ {
		scheduler.workerQueues[i] = NewChaseLevQueue(config.QueueSize)
		scheduler.workerStatus[i] = WorkerSchedulerStatus{
			CurrentTask:  -1,
			LastActivity: time.Now(),
		}
	}

	// Start metrics collection if enabled
	if config.MetricsInterval > 0 {
		scheduler.wg.Add(1)
		go scheduler.metricsCollector()
	}

	return scheduler
}

// AddTasks adds tasks to the scheduler for execution.
func (s *WorkStealingScheduler) AddTasks(tasks []common.Task) error {
	if atomic.LoadInt32(&s.shutdown) != 0 {
		return common.ErrEngineShutdown
	}

	s.taskMutex.Lock()
	defer s.taskMutex.Unlock()

	// First pass: add all tasks and build dependency graph
	for _, task := range tasks {
		s.allTasks[task.ID()] = task
		s.taskStatus[task.ID()] = TaskStatusPending
		s.dependencies[task.ID()] = task.Dependencies()

		// Build reverse dependencies
		for _, depID := range task.Dependencies() {
			s.dependents[depID] = append(s.dependents[depID], task.ID())
		}
	}

	// Second pass: detect cycles and build execution order
	if s.config.DependencyAnalysis {
		if err := s.detectCycles(tasks); err != nil {
			return err
		}
	}

	// Third pass: add ready tasks to the ready queue
	readyTasks := s.findReadyTasks(tasks)

	s.readyMutex.Lock()
	for _, task := range readyTasks {
		priority := s.calculateTaskPriority(task)
		s.readyQueue.Push(task, priority)
		s.taskStatus[task.ID()] = TaskStatusReady
	}
	s.readyMutex.Unlock()

	return nil
}

// NextTask returns the next task for the specified worker to execute.
func (s *WorkStealingScheduler) NextTask(workerID int) (common.Task, bool) {
	if atomic.LoadInt32(&s.shutdown) != 0 {
		return nil, false
	}

	if workerID < 0 || workerID >= len(s.workerQueues) {
		return nil, false
	}

	// First, try to get a task from the worker's own queue
	if task, ok := s.workerQueues[workerID].PopBottom(); ok {
		s.assignTaskToWorker(task, workerID)
		return task, true
	}

	// If worker's queue is empty, try to get from the global ready queue
	s.readyMutex.Lock()
	if task, ok := s.readyQueue.Pop(); ok {
		s.readyMutex.Unlock()
		s.assignTaskToWorker(task, workerID)
		return task, true
	}
	s.readyMutex.Unlock()

	// If still no task and work stealing is enabled, try to steal from other workers
	if s.config.StealingEnabled {
		if task, ok := s.stealTask(workerID); ok {
			s.assignTaskToWorker(task, workerID)
			atomic.AddInt64(&s.metrics.WorkStealingEvents, 1)
			return task, true
		}
	}

	return nil, false
}

// CompleteTask marks a task as completed and handles dependencies.
func (s *WorkStealingScheduler) CompleteTask(taskID common.TaskID, result common.ExecutionResult) error {
	s.taskMutex.Lock()
	defer s.taskMutex.Unlock()

	// Update task status
	s.taskStatus[taskID] = TaskStatusCompleted

	// Update worker status
	for i := range s.workerStatus {
		if s.workerStatus[i].CurrentTask == taskID {
			s.workerStatus[i].CurrentTask = -1
			s.workerStatus[i].TasksCompleted++
			s.workerStatus[i].LastActivity = time.Now()
			break
		}
	}

	// Check if any dependent tasks are now ready
	readyTasks := s.checkDependentTasks(taskID)

	// Add newly ready tasks to appropriate queues
	if len(readyTasks) > 0 {
		s.scheduleReadyTasks(readyTasks)
	}

	return nil
}

// AbortTask marks a task as aborted and schedules it for re-execution.
func (s *WorkStealingScheduler) AbortTask(taskID common.TaskID, dependency common.TaskID) error {
	s.taskMutex.Lock()
	defer s.taskMutex.Unlock()

	// Update task status
	s.taskStatus[taskID] = TaskStatusAborted

	// Add the new dependency if it's not already present
	deps := s.dependencies[taskID]
	hasDependency := false
	for _, dep := range deps {
		if dep == dependency {
			hasDependency = true
			break
		}
	}

	if !hasDependency {
		s.dependencies[taskID] = append(deps, dependency)
		s.dependents[dependency] = append(s.dependents[dependency], taskID)
	}

	// Reset task status to pending
	s.taskStatus[taskID] = TaskStatusPending

	// Update worker status
	for i := range s.workerStatus {
		if s.workerStatus[i].CurrentTask == taskID {
			s.workerStatus[i].CurrentTask = -1
			s.workerStatus[i].LastActivity = time.Now()
			break
		}
	}

	return nil
}

// Helper methods

// detectCycles detects circular dependencies in the task graph.
func (s *WorkStealingScheduler) detectCycles(tasks []common.Task) error {
	visited := make(map[common.TaskID]bool)
	recStack := make(map[common.TaskID]bool)

	for _, task := range tasks {
		if !visited[task.ID()] {
			if s.hasCycle(task.ID(), visited, recStack) {
				return common.ErrCircularDependency
			}
		}
	}

	return nil
}

// hasCycle performs DFS to detect cycles in the dependency graph.
func (s *WorkStealingScheduler) hasCycle(taskID common.TaskID, visited, recStack map[common.TaskID]bool) bool {
	visited[taskID] = true
	recStack[taskID] = true

	for _, depID := range s.dependencies[taskID] {
		if !visited[depID] {
			if s.hasCycle(depID, visited, recStack) {
				return true
			}
		} else if recStack[depID] {
			return true
		}
	}

	recStack[taskID] = false
	return false
}

// findReadyTasks finds tasks that have no pending dependencies.
func (s *WorkStealingScheduler) findReadyTasks(tasks []common.Task) []common.Task {
	var readyTasks []common.Task

	for _, task := range tasks {
		if s.isTaskReady(task.ID()) {
			readyTasks = append(readyTasks, task)
		}
	}

	return readyTasks
}

// isTaskReady checks if a task is ready to be executed.
func (s *WorkStealingScheduler) isTaskReady(taskID common.TaskID) bool {
	deps := s.dependencies[taskID]
	for _, depID := range deps {
		if status, exists := s.taskStatus[depID]; !exists || status != TaskStatusCompleted {
			return false
		}
	}
	return true
}

// calculateTaskPriority calculates the priority of a task for scheduling.
func (s *WorkStealingScheduler) calculateTaskPriority(task common.Task) float64 {
	// Base priority on estimated execution time and gas usage
	priority := float64(task.EstimatedGas())

	// Add dependency-based priority
	dependentCount := float64(len(s.dependents[task.ID()]))
	priority += dependentCount * 1000 // Give higher priority to tasks with more dependents

	// Add adaptive priority based on current system state
	if s.config.AdaptiveScheduling {
		priority *= s.getAdaptivePriorityMultiplier()
	}

	return priority
}

// getAdaptivePriorityMultiplier returns a multiplier based on current system state.
func (s *WorkStealingScheduler) getAdaptivePriorityMultiplier() float64 {
	// Simple adaptive algorithm - can be made more sophisticated
	queueDepth := s.readyQueue.Size()
	if queueDepth > 100 {
		return 0.5 // Lower priority when queue is full
	} else if queueDepth < 10 {
		return 2.0 // Higher priority when queue is nearly empty
	}
	return 1.0
}

// assignTaskToWorker assigns a task to a specific worker.
func (s *WorkStealingScheduler) assignTaskToWorker(task common.Task, workerID int) {
	s.workerMutex.Lock()
	s.workerStatus[workerID].CurrentTask = task.ID()
	s.workerStatus[workerID].TasksAssigned++
	s.workerStatus[workerID].LastActivity = time.Now()
	s.workerMutex.Unlock()

	s.taskMutex.Lock()
	s.taskStatus[task.ID()] = TaskStatusRunning
	s.taskMutex.Unlock()
}

// stealTask attempts to steal a task from another worker's queue.
func (s *WorkStealingScheduler) stealTask(workerID int) (common.Task, bool) {
	// Try to steal from a random worker
	for i := 0; i < len(s.workerQueues); i++ {
		victimID := (workerID + i + 1) % len(s.workerQueues)
		if task, ok := s.workerQueues[victimID].PopTop(); ok {
			return task, true
		}
	}
	return nil, false
}

// checkDependentTasks checks which dependent tasks are now ready after task completion.
func (s *WorkStealingScheduler) checkDependentTasks(completedTaskID common.TaskID) []common.Task {
	var readyTasks []common.Task

	for _, dependentID := range s.dependents[completedTaskID] {
		if s.isTaskReady(dependentID) && s.taskStatus[dependentID] == TaskStatusPending {
			if task, exists := s.allTasks[dependentID]; exists {
				readyTasks = append(readyTasks, task)
			}
		}
	}

	return readyTasks
}

// scheduleReadyTasks adds ready tasks to appropriate queues.
func (s *WorkStealingScheduler) scheduleReadyTasks(tasks []common.Task) {
	if s.config.LoadBalancing {
		s.distributeTasksEvenly(tasks)
	} else {
		s.addTasksToGlobalQueue(tasks)
	}
}

// distributeTasksEvenly distributes tasks evenly across worker queues.
func (s *WorkStealingScheduler) distributeTasksEvenly(tasks []common.Task) {
	// Sort workers by current queue size to balance load
	type workerLoad struct {
		id   int
		load int
	}

	workers := make([]workerLoad, len(s.workerQueues))
	for i := range workers {
		workers[i] = workerLoad{id: i, load: s.workerQueues[i].Size()}
	}

	sort.Slice(workers, func(i, j int) bool {
		return workers[i].load < workers[j].load
	})

	// Distribute tasks to workers with least load
	for i, task := range tasks {
		workerID := workers[i%len(workers)].id
		if s.workerQueues[workerID].PushBottom(task) {
			s.taskStatus[task.ID()] = TaskStatusReady
		} else {
			// If worker queue is full, add to global queue
			s.readyMutex.Lock()
			priority := s.calculateTaskPriority(task)
			s.readyQueue.Push(task, priority)
			s.taskStatus[task.ID()] = TaskStatusReady
			s.readyMutex.Unlock()
		}
	}
}

// addTasksToGlobalQueue adds tasks to the global ready queue.
func (s *WorkStealingScheduler) addTasksToGlobalQueue(tasks []common.Task) {
	s.readyMutex.Lock()
	defer s.readyMutex.Unlock()

	for _, task := range tasks {
		priority := s.calculateTaskPriority(task)
		s.readyQueue.Push(task, priority)
		s.taskStatus[task.ID()] = TaskStatusReady
	}
}

// GetStatus returns the current scheduling status.
func (s *WorkStealingScheduler) GetStatus() SchedulerStatus {
	s.taskMutex.RLock()
	defer s.taskMutex.RUnlock()

	status := SchedulerStatus{
		TotalTasks:   len(s.allTasks),
		WorkerStatus: make(map[int]WorkerSchedulerStatus),
	}

	// Count tasks by status
	for _, taskStatus := range s.taskStatus {
		switch taskStatus {
		case TaskStatusPending:
			status.PendingTasks++
		case TaskStatusReady:
			status.PendingTasks++
		case TaskStatusRunning:
			status.RunningTasks++
		case TaskStatusCompleted:
			status.CompletedTasks++
		case TaskStatusAborted, TaskStatusFailed:
			status.FailedTasks++
		}
	}

	// Copy worker status
	s.workerMutex.RLock()
	for i, ws := range s.workerStatus {
		status.WorkerStatus[i] = ws
	}
	s.workerMutex.RUnlock()

	return status
}

// GetMetrics returns scheduling metrics.
func (s *WorkStealingScheduler) GetMetrics() SchedulerMetrics {
	return s.metrics
}

// metricsCollector runs in the background to collect metrics.
func (s *WorkStealingScheduler) metricsCollector() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.MetricsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.updateMetrics()
		default:
			if atomic.LoadInt32(&s.shutdown) != 0 {
				return
			}
		}
	}
}

// updateMetrics updates the current metrics.
func (s *WorkStealingScheduler) updateMetrics() {
	now := time.Now()

	// Update queue depth
	s.metrics.TaskQueueDepth = s.readyQueue.Size()

	// Calculate load balance efficiency
	s.metrics.LoadBalanceEfficiency = s.calculateLoadBalanceEfficiency()

	s.lastMetricsUpdate = now
}

// calculateLoadBalanceEfficiency calculates how evenly work is distributed.
func (s *WorkStealingScheduler) calculateLoadBalanceEfficiency() float64 {
	if len(s.workerQueues) == 0 {
		return 1.0
	}

	// Calculate standard deviation of queue sizes
	var total, mean, variance float64
	sizes := make([]float64, len(s.workerQueues))

	for i, queue := range s.workerQueues {
		size := float64(queue.Size())
		sizes[i] = size
		total += size
	}

	mean = total / float64(len(sizes))

	for _, size := range sizes {
		variance += (size - mean) * (size - mean)
	}
	variance /= float64(len(sizes))

	// Efficiency is inversely related to variance
	// Perfect efficiency (1.0) when all queues have the same size
	if variance == 0 {
		return 1.0
	}

	return 1.0 / (1.0 + variance/mean)
}

// Close shuts down the scheduler.
func (s *WorkStealingScheduler) Close() error {
	atomic.StoreInt32(&s.shutdown, 1)
	s.wg.Wait()
	return nil
}

// Default configuration values.
const (
	DefaultNumWorkers      = 8
	DefaultQueueSize       = 256
	DefaultMetricsInterval = time.Second
)
