// Package executor provides parallel task execution for the PEVM engine.
package executor

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/longcipher/pevm-go/common"
	"github.com/longcipher/pevm-go/storage"
)

// Executor defines the interface for executing tasks.
type Executor interface {
	// Execute executes a single task with the given storage.
	Execute(ctx context.Context, task common.Task, storage storage.Storage) common.ExecutionResult

	// EstimateExecution provides an estimate of the execution cost.
	EstimateExecution(task common.Task) ExecutionEstimate

	// Capabilities returns the capabilities of this executor.
	Capabilities() ExecutorCapabilities

	// Configure updates the executor configuration.
	Configure(config ExecutorConfig) error
}

// ExecutionEstimate provides estimates about task execution.
type ExecutionEstimate struct {
	Gas      uint64        // Estimated gas consumption
	Duration time.Duration // Estimated execution time
	Memory   int64         // Estimated memory usage
	Priority float64       // Suggested priority (higher = more important)
}

// ExecutorCapabilities describes what an executor can do.
type ExecutorCapabilities struct {
	SupportsConcurrency  bool     // Whether the executor supports concurrent execution
	SupportsEstimation   bool     // Whether the executor can provide estimates
	SupportedTaskTypes   []string // Types of tasks this executor can handle
	MaxConcurrency       int      // Maximum number of concurrent executions
	ResourceRequirements ResourceRequirements
}

// ResourceRequirements describes the resources needed by an executor.
type ResourceRequirements struct {
	MinMemory int64 // Minimum memory required (bytes)
	MaxMemory int64 // Maximum memory that can be used (bytes)
	CPUCores  int   // Number of CPU cores needed
}

// ExecutorConfig contains configuration for executors.
type ExecutorConfig struct {
	MaxConcurrency  int                    // Maximum concurrent executions
	Timeout         time.Duration          // Execution timeout
	MemoryLimit     int64                  // Memory limit per execution
	EnableProfiling bool                   // Whether to enable profiling
	CustomSettings  map[string]interface{} // Executor-specific settings
	ResourceManager ResourceManager        // Resource management
}

// ResourceManager manages system resources for executors.
type ResourceManager interface {
	// AllocateResources allocates resources for task execution.
	AllocateResources(estimate ExecutionEstimate) (ResourceAllocation, error)

	// ReleaseResources releases previously allocated resources.
	ReleaseResources(allocation ResourceAllocation) error

	// GetAvailableResources returns currently available resources.
	GetAvailableResources() AvailableResources
}

// ResourceAllocation represents allocated resources.
type ResourceAllocation struct {
	ID       int64 // Unique allocation ID
	Memory   int64 // Allocated memory
	CPUCores []int // Allocated CPU cores
	Timeout  time.Duration
}

// AvailableResources represents currently available system resources.
type AvailableResources struct {
	Memory   int64   // Available memory
	CPUCores []int   // Available CPU cores
	CPUUsage float64 // Current CPU usage (0.0-1.0)
	LoadAvg  float64 // System load average
}

// ParallelExecutor manages parallel execution of tasks using worker pools.
type ParallelExecutor struct {
	// Configuration
	config ExecutorConfig

	// Worker management
	workers    []*Worker
	workerPool sync.Pool
	scheduler  Scheduler

	// Resource management
	resourceMgr ResourceManager

	// Statistics
	stats           ExecutorStats
	statsLastUpdate time.Time
	statsMutex      sync.RWMutex

	// Lifecycle management
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	shutdown int32
}

// Worker represents a single execution worker.
type Worker struct {
	ID            int
	parallelExec  *ParallelExecutor // Reference to parent executor
	storage       storage.Storage
	currentTask   atomic.Value // *common.Task
	stats         WorkerStats
	lastActivity  time.Time
	resourceAlloc ResourceAllocation
}

// WorkerStats contains statistics for a single worker.
type WorkerStats struct {
	TasksExecuted  int64         // Total tasks executed
	TasksSucceeded int64         // Tasks that succeeded
	TasksFailed    int64         // Tasks that failed
	TotalGasUsed   uint64        // Total gas consumed
	TotalDuration  time.Duration // Total execution time
	AvgDuration    time.Duration // Average execution time
	LastError      error         // Last error encountered
	LastUpdate     time.Time     // Last statistics update
}

// ExecutorStats contains overall executor statistics.
type ExecutorStats struct {
	ActiveWorkers    int           // Number of active workers
	IdleWorkers      int           // Number of idle workers
	TotalTasks       int64         // Total tasks processed
	SuccessfulTasks  int64         // Successfully executed tasks
	FailedTasks      int64         // Failed tasks
	AvgExecutionTime time.Duration // Average execution time
	TotalGasUsed     uint64        // Total gas consumed
	MemoryUsage      int64         // Current memory usage
	CPUUsage         float64       // Current CPU usage
	Throughput       float64       // Tasks per second
	ErrorRate        float64       // Error rate (0.0-1.0)
}

// Scheduler interface for worker coordination.
type Scheduler interface {
	NextTask(workerID int) (common.Task, bool)
	CompleteTask(taskID common.TaskID, result common.ExecutionResult) error
	AbortTask(taskID common.TaskID, dependency common.TaskID) error
}

// NewParallelExecutor creates a new parallel executor.
func NewParallelExecutor(config ExecutorConfig, scheduler Scheduler, resourceMgr ResourceManager) *ParallelExecutor {
	ctx, cancel := context.WithCancel(context.Background())

	executor := &ParallelExecutor{
		config:      config,
		scheduler:   scheduler,
		resourceMgr: resourceMgr,
		ctx:         ctx,
		cancel:      cancel,
		workers:     make([]*Worker, config.MaxConcurrency),
	}

	// Initialize worker pool
	executor.workerPool = sync.Pool{
		New: func() interface{} {
			return &Worker{
				parallelExec: executor,
				storage:      nil, // Will be set when task is assigned
			}
		},
	}

	// Start workers
	for i := 0; i < config.MaxConcurrency; i++ {
		worker := &Worker{
			ID:           i,
			parallelExec: executor,
		}
		executor.workers[i] = worker

		executor.wg.Add(1)
		go executor.workerLoop(worker)
	}

	// Start statistics collector
	executor.wg.Add(1)
	go executor.statsCollector()

	return executor
}

// workerLoop is the main loop for each worker.
func (pe *ParallelExecutor) workerLoop(worker *Worker) {
	defer pe.wg.Done()

	for {
		select {
		case <-pe.ctx.Done():
			return
		default:
			// Try to get a task from the scheduler
			task, ok := pe.scheduler.NextTask(worker.ID)
			if !ok {
				// No task available, sleep briefly
				time.Sleep(time.Microsecond * 100)
				continue
			}

			// Execute the task
			pe.executeTask(worker, task)
		}
	}
}

// executeTask executes a single task on the given worker.
func (pe *ParallelExecutor) executeTask(worker *Worker, task common.Task) {
	startTime := time.Now()
	worker.lastActivity = startTime
	worker.currentTask.Store(&task)

	// Allocate resources for execution
	estimate := ExecutionEstimate{
		Gas:      task.EstimatedGas(),
		Duration: task.EstimatedDuration(),
		Memory:   1024 * 1024, // Default 1MB
	}

	allocation, err := pe.resourceMgr.AllocateResources(estimate)
	if err != nil {
		// Resource allocation failed
		result := common.ExecutionResult{
			TaskID:    task.ID(),
			Success:   false,
			Error:     err,
			Duration:  time.Since(startTime),
			WorkerID:  worker.ID,
			Timestamp: time.Now(),
		}
		if completeErr := pe.scheduler.CompleteTask(task.ID(), result); completeErr != nil {
			// Log the error but don't fail - task execution already failed
			_ = completeErr // Explicitly ignore for now
		}
		return
	}

	worker.resourceAlloc = allocation

	// Create execution context with timeout
	ctx, cancel := context.WithTimeout(pe.ctx, pe.config.Timeout)
	defer cancel()

	// Execute the task
	var result common.ExecutionResult

	// Set CPU affinity if configured
	if len(allocation.CPUCores) > 0 {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}

	// Create storage view for this execution
	storageView := pe.createStorageView(task)

	// Execute with panic recovery
	func() {
		defer func() {
			if r := recover(); r != nil {
				result = common.ExecutionResult{
					TaskID:    task.ID(),
					Success:   false,
					Error:     common.ErrWorkerPanic,
					Duration:  time.Since(startTime),
					WorkerID:  worker.ID,
					Timestamp: time.Now(),
				}
			}
		}()

		// Execute the task with the appropriate executor
		taskExecutor := pe.selectExecutor(task)
		result = taskExecutor.Execute(ctx, task, storageView)
		result.WorkerID = worker.ID
		result.Timestamp = time.Now()
		result.Duration = time.Since(startTime)
	}()

	// Release resources
	if releaseErr := pe.resourceMgr.ReleaseResources(allocation); releaseErr != nil {
		// Log the error but continue - task has completed
		_ = releaseErr // Explicitly ignore for now
	}

	// Update worker statistics
	pe.updateWorkerStats(worker, result)

	// Report completion to scheduler
	if result.Success {
		if completeErr := pe.scheduler.CompleteTask(task.ID(), result); completeErr != nil {
			// Log the error but continue - task has completed successfully
			_ = completeErr // Explicitly ignore for now
		}
	} else {
		// Check if this is a dependency conflict
		if dependency := pe.extractDependency(result.Error); dependency != -1 {
			if abortErr := pe.scheduler.AbortTask(task.ID(), common.TaskID(dependency)); abortErr != nil {
				// Log the error but continue
				_ = abortErr // Explicitly ignore for now
			}
		} else {
			if completeErr := pe.scheduler.CompleteTask(task.ID(), result); completeErr != nil {
				// Log the error but continue
				_ = completeErr // Explicitly ignore for now
			}
		}
	}

	worker.currentTask.Store((*common.Task)(nil))
}

// createStorageView creates a storage view for task execution.
func (pe *ParallelExecutor) createStorageView(task common.Task) storage.Storage {
	// For now, return a simple storage implementation
	// In a real implementation, this would create a view specific to the task's execution context
	return storage.NewLockFreeStorage(storage.Config{
		BucketCount: 1024,
		GCThreshold: 10000,
	})
}

// selectExecutor selects the appropriate executor for a task.
func (pe *ParallelExecutor) selectExecutor(task common.Task) Executor {
	// For now, return a default executor
	// In a real implementation, this would select based on task type
	return &DefaultExecutor{}
}

// extractDependency extracts dependency information from an error.
func (pe *ParallelExecutor) extractDependency(err error) int {
	// Parse error to extract dependency information
	// This is a simplified implementation
	if err != nil && err.Error() == "dependency_conflict" {
		return 1 // Return dependency task ID
	}
	return -1
}

// updateWorkerStats updates statistics for a worker.
func (pe *ParallelExecutor) updateWorkerStats(worker *Worker, result common.ExecutionResult) {
	atomic.AddInt64(&worker.stats.TasksExecuted, 1)

	if result.Success {
		atomic.AddInt64(&worker.stats.TasksSucceeded, 1)
		atomic.AddUint64(&worker.stats.TotalGasUsed, result.GasUsed)
	} else {
		atomic.AddInt64(&worker.stats.TasksFailed, 1)
		worker.stats.LastError = result.Error
	}

	// Update timing statistics
	totalDuration := time.Duration(atomic.LoadInt64((*int64)(&worker.stats.TotalDuration))) + result.Duration
	atomic.StoreInt64((*int64)(&worker.stats.TotalDuration), int64(totalDuration))

	tasksExecuted := atomic.LoadInt64(&worker.stats.TasksExecuted)
	if tasksExecuted > 0 {
		avgDuration := totalDuration / time.Duration(tasksExecuted)
		atomic.StoreInt64((*int64)(&worker.stats.AvgDuration), int64(avgDuration))
	}

	worker.stats.LastUpdate = time.Now()
}

// statsCollector runs in background to collect overall statistics.
func (pe *ParallelExecutor) statsCollector() {
	defer pe.wg.Done()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pe.ctx.Done():
			return
		case <-ticker.C:
			pe.updateOverallStats()
		}
	}
}

// updateOverallStats updates the overall executor statistics.
func (pe *ParallelExecutor) updateOverallStats() {
	pe.statsMutex.Lock()
	defer pe.statsMutex.Unlock()

	now := time.Now()
	var totalTasks, successfulTasks, failedTasks int64
	var totalGas uint64
	var totalDuration time.Duration
	activeWorkers := 0

	for _, worker := range pe.workers {
		if task := worker.currentTask.Load(); task != nil && task.(*common.Task) != nil {
			activeWorkers++
		}

		totalTasks += atomic.LoadInt64(&worker.stats.TasksExecuted)
		successfulTasks += atomic.LoadInt64(&worker.stats.TasksSucceeded)
		failedTasks += atomic.LoadInt64(&worker.stats.TasksFailed)
		totalGas += atomic.LoadUint64(&worker.stats.TotalGasUsed)
		totalDuration += time.Duration(atomic.LoadInt64((*int64)(&worker.stats.TotalDuration)))
	}

	pe.stats.ActiveWorkers = activeWorkers
	pe.stats.IdleWorkers = len(pe.workers) - activeWorkers
	pe.stats.TotalTasks = totalTasks
	pe.stats.SuccessfulTasks = successfulTasks
	pe.stats.FailedTasks = failedTasks
	pe.stats.TotalGasUsed = totalGas

	// Calculate average execution time
	if totalTasks > 0 {
		pe.stats.AvgExecutionTime = totalDuration / time.Duration(totalTasks)
		pe.stats.ErrorRate = float64(failedTasks) / float64(totalTasks)
	}

	// Calculate throughput
	if !pe.statsLastUpdate.IsZero() {
		elapsed := now.Sub(pe.statsLastUpdate).Seconds()
		if elapsed > 0 {
			tasksDelta := totalTasks - pe.stats.TotalTasks
			pe.stats.Throughput = float64(tasksDelta) / elapsed
		}
	}

	// Get resource usage
	if pe.resourceMgr != nil {
		available := pe.resourceMgr.GetAvailableResources()
		pe.stats.CPUUsage = available.CPUUsage
		pe.stats.MemoryUsage = available.Memory
	}

	pe.statsLastUpdate = now
}

// GetStats returns current executor statistics.
func (pe *ParallelExecutor) GetStats() ExecutorStats {
	pe.statsMutex.RLock()
	defer pe.statsMutex.RUnlock()
	return pe.stats
}

// GetWorkerStats returns statistics for all workers.
func (pe *ParallelExecutor) GetWorkerStats() []WorkerStats {
	stats := make([]WorkerStats, len(pe.workers))
	for i, worker := range pe.workers {
		stats[i] = WorkerStats{
			TasksExecuted:  atomic.LoadInt64(&worker.stats.TasksExecuted),
			TasksSucceeded: atomic.LoadInt64(&worker.stats.TasksSucceeded),
			TasksFailed:    atomic.LoadInt64(&worker.stats.TasksFailed),
			TotalGasUsed:   atomic.LoadUint64(&worker.stats.TotalGasUsed),
			TotalDuration:  time.Duration(atomic.LoadInt64((*int64)(&worker.stats.TotalDuration))),
			AvgDuration:    time.Duration(atomic.LoadInt64((*int64)(&worker.stats.AvgDuration))),
			LastError:      worker.stats.LastError,
			LastUpdate:     worker.stats.LastUpdate,
		}
	}
	return stats
}

// Shutdown gracefully shuts down the executor.
func (pe *ParallelExecutor) Shutdown(timeout time.Duration) error {
	atomic.StoreInt32(&pe.shutdown, 1)

	// Cancel context to signal workers to stop
	pe.cancel()

	// Wait for workers to finish with timeout
	done := make(chan struct{})
	go func() {
		pe.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return common.ErrExecutionTimeout
	}
}

// DefaultExecutor provides a basic implementation of the Executor interface.
type DefaultExecutor struct {
	config ExecutorConfig
}

// Execute implements the Executor interface.
func (de *DefaultExecutor) Execute(ctx context.Context, task common.Task, storage storage.Storage) common.ExecutionResult {
	// This is a placeholder implementation
	// Real executors would implement actual task execution logic

	select {
	case <-ctx.Done():
		return common.ExecutionResult{
			TaskID:  task.ID(),
			Success: false,
			Error:   ctx.Err(),
		}
	case <-time.After(time.Millisecond * 10): // Simulate execution time
		return common.ExecutionResult{
			TaskID:  task.ID(),
			Success: true,
			GasUsed: task.EstimatedGas(),
		}
	}
}

// EstimateExecution implements the Executor interface.
func (de *DefaultExecutor) EstimateExecution(task common.Task) ExecutionEstimate {
	return ExecutionEstimate{
		Gas:      task.EstimatedGas(),
		Duration: task.EstimatedDuration(),
		Memory:   1024 * 1024, // 1MB default
		Priority: 1.0,
	}
}

// Capabilities implements the Executor interface.
func (de *DefaultExecutor) Capabilities() ExecutorCapabilities {
	return ExecutorCapabilities{
		SupportsConcurrency: true,
		SupportsEstimation:  true,
		SupportedTaskTypes:  []string{"default"},
		MaxConcurrency:      runtime.NumCPU(),
		ResourceRequirements: ResourceRequirements{
			MinMemory: 1024 * 1024,       // 1MB
			MaxMemory: 100 * 1024 * 1024, // 100MB
			CPUCores:  1,
		},
	}
}

// Configure implements the Executor interface.
func (de *DefaultExecutor) Configure(config ExecutorConfig) error {
	de.config = config
	return nil
}
