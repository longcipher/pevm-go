// Package main provides examples of using the PEVM parallel execution engine.
package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	commonpevm "github.com/longcipher/pevm-go/common"
	"github.com/longcipher/pevm-go/engine"
)

// ExampleTask implements the Task interface for demonstration.
type ExampleTask struct {
	id           commonpevm.TaskID
	dependencies []commonpevm.TaskID
	gasEstimate  uint64
	duration     time.Duration
	sender       common.Address
	hash         common.Hash
	operations   []Operation
}

// Operation represents a read or write operation.
type Operation struct {
	Type  OperationType
	Key   commonpevm.StorageKey
	Value interface{}
}

// OperationType defines the type of operation.
type OperationType int

const (
	OpRead OperationType = iota
	OpWrite
)

// Implement the Task interface
func (et *ExampleTask) ID() commonpevm.TaskID {
	return et.id
}

func (et *ExampleTask) Dependencies() []commonpevm.TaskID {
	return et.dependencies
}

func (et *ExampleTask) EstimatedGas() uint64 {
	return et.gasEstimate
}

func (et *ExampleTask) EstimatedDuration() time.Duration {
	return et.duration
}

func (et *ExampleTask) Sender() common.Address {
	return et.sender
}

func (et *ExampleTask) Hash() common.Hash {
	return et.hash
}

func main() {
	// Example 1: Basic parallel execution
	fmt.Println("=== Example 1: Basic Parallel Execution ===")
	basicParallelExecution()

	// Example 2: High-conflict workload
	fmt.Println("\n=== Example 2: High-Conflict Workload ===")
	highConflictWorkload()

	// Example 3: EVM-like transaction simulation
	fmt.Println("\n=== Example 3: EVM Transaction Simulation ===")
	evmTransactionSimulation()

	// Example 4: Performance benchmarking
	fmt.Println("\n=== Example 4: Performance Benchmarking ===")
	performanceBenchmark()

	// Example 5: Custom configuration
	fmt.Println("\n=== Example 5: Custom Configuration ===")
	customConfiguration()
}

// basicParallelExecution demonstrates basic usage of the PEVM engine.
func basicParallelExecution() {
	// Create engine with default configuration
	config := engine.DefaultConfig()
	config.MaxWorkers = 4
	config.EnableMetrics = true

	eng := engine.NewEngine(config)
	defer eng.Stop()

	// Create sample tasks
	tasks := createSampleTasks(10, false)

	// Execute tasks in parallel
	ctx := context.Background()
	options := engine.ExecutionOptions{
		ExecutionOptions: commonpevm.ExecutionOptions{
			MaxWorkers:    4,
			EnableMetrics: true,
			Timeout:       time.Minute * 5,
		},
	}

	result, err := eng.Execute(ctx, tasks, options)
	if err != nil {
		log.Fatalf("Execution failed: %v", err)
	}

	// Print results
	fmt.Printf("Total tasks: %d\n", len(tasks))
	fmt.Printf("Execution time: %v\n", result.TotalDuration)
	fmt.Printf("Parallel efficiency: %.2f%%\n", result.ParallelEfficiency*100)
	fmt.Printf("Conflicts detected: %d\n", result.ConflictsDetected)
}

// highConflictWorkload demonstrates handling of high-conflict scenarios.
func highConflictWorkload() {
	config := engine.DefaultConfig()
	config.MaxWorkers = 8
	config.EnableMetrics = true

	eng := engine.NewEngine(config)
	defer eng.Stop()

	// Create tasks with intentional conflicts
	tasks := createConflictingTasks(20)

	ctx := context.Background()
	options := engine.ExecutionOptions{
		ExecutionOptions: commonpevm.ExecutionOptions{
			MaxWorkers:    8,
			EnableMetrics: true,
			Timeout:       time.Minute * 5,
		},
	}

	start := time.Now()
	result, err := eng.Execute(ctx, tasks, options)
	if err != nil {
		log.Fatalf("Execution failed: %v", err)
	}

	fmt.Printf("High-conflict workload completed in %v\n", time.Since(start))
	fmt.Printf("Conflicts detected: %d\n", result.ConflictsDetected)
	fmt.Printf("Retries performed: %d\n", result.RetriesPerformed)
	fmt.Printf("Final efficiency: %.2f%%\n", result.ParallelEfficiency*100)
}

// evmTransactionSimulation simulates EVM-like transaction processing.
func evmTransactionSimulation() {
	config := engine.DefaultConfig()
	config.MaxWorkers = 6
	config.EnableAdaptive = true

	eng := engine.NewEngine(config)
	defer eng.Stop()

	// Create EVM-like transactions
	tasks := createEVMTransactions(50)

	ctx := context.Background()
	options := engine.ExecutionOptions{
		ExecutionOptions: commonpevm.ExecutionOptions{
			MaxWorkers:    6,
			EnableMetrics: true,
			Timeout:       time.Minute * 10,
		},
	}

	result, err := eng.Execute(ctx, tasks, options)
	if err != nil {
		log.Fatalf("EVM simulation failed: %v", err)
	}

	// Calculate throughput
	throughput := float64(len(tasks)) / result.TotalDuration.Seconds()

	fmt.Printf("Processed %d transactions in %v\n", len(tasks), result.TotalDuration)
	fmt.Printf("Throughput: %.2f tx/s\n", throughput)
	fmt.Printf("Average gas per transaction: %.0f\n",
		float64(result.Metrics.StorageMetrics.TotalVersions)/float64(len(tasks)))
}

// performanceBenchmark runs performance benchmarks with different configurations.
func performanceBenchmark() {
	fmt.Println("Running performance benchmark...")

	workerCounts := []int{1, 2, 4, 8}
	taskCounts := []int{100, 500, 1000}

	for _, workers := range workerCounts {
		for _, taskCount := range taskCounts {
			duration := benchmarkConfiguration(workers, taskCount)
			throughput := float64(taskCount) / duration.Seconds()

			fmt.Printf("Workers: %d, Tasks: %d, Duration: %v, Throughput: %.2f tx/s\n",
				workers, taskCount, duration, throughput)
		}
	}
}

// customConfiguration demonstrates advanced configuration options.
func customConfiguration() {
	config := engine.Config{
		MaxWorkers:       16,
		TaskTimeout:      time.Second * 30,
		ExecutionTimeout: time.Minute * 15,
		SchedulerType:    engine.SchedulerTypeWorkStealing,
		StorageType:      engine.StorageTypeLockFree,
		ValidationType:   engine.ValidationTypeEager,
		ExecutorType:     engine.ExecutorTypeParallel,
		EnableAdaptive:   true,
		EnableProfiling:  true,
		EnableMetrics:    true,
		MetricsInterval:  time.Millisecond * 500,
		MemoryLimit:      2 * 1024 * 1024 * 1024, // 2GB
		CPUAffinity:      true,
		NUMAAware:        true,
		CustomConfig: map[string]interface{}{
			"optimization_level": "aggressive",
			"cache_size":         "256MB",
			"gc_threshold":       50000,
		},
	}

	eng := engine.NewEngine(config)
	defer eng.Stop()

	tasks := createSampleTasks(100, true)

	ctx := context.Background()
	options := engine.ExecutionOptions{
		ExecutionOptions: commonpevm.ExecutionOptions{
			MaxWorkers:     16,
			EnableMetrics:  true,
			Timeout:        time.Minute * 15,
			WorkerAffinity: true,
			MemoryLimit:    2 * 1024 * 1024 * 1024,
			ValidationMode: commonpevm.ValidationModeEager,
		},
	}

	result, err := eng.Execute(ctx, tasks, options)
	if err != nil {
		log.Fatalf("Custom configuration execution failed: %v", err)
	}

	fmt.Printf("Custom configuration results:\n")
	fmt.Printf("  Execution time: %v\n", result.TotalDuration)
	fmt.Printf("  Memory usage: %d MB\n", result.Metrics.ResourceUtilization.MemoryUsage/(1024*1024))
	fmt.Printf("  CPU utilization: %.2f%%\n", result.Metrics.ResourceUtilization.CPUUsage*100)
	fmt.Printf("  Effective parallelism: %.2fx\n", result.Metrics.EffectiveParallelism)
}

// Helper functions

func createSampleTasks(count int, withDependencies bool) []commonpevm.Task {
	tasks := make([]commonpevm.Task, count)

	for i := 0; i < count; i++ {
		var deps []commonpevm.TaskID
		if withDependencies && i > 0 {
			// Add dependency on previous task
			deps = append(deps, commonpevm.TaskID(i-1))
		}

		task := &ExampleTask{
			id:           commonpevm.TaskID(i),
			dependencies: deps,
			gasEstimate:  uint64(21000 + i*100), // Base gas + variable component
			duration:     time.Millisecond * time.Duration(10+i%20),
			sender:       common.BigToAddress(big.NewInt(int64(i % 10))),
			hash:         common.BigToHash(big.NewInt(int64(i))),
			operations:   createOperations(i),
		}

		tasks[i] = task
	}

	return tasks
}

func createConflictingTasks(count int) []commonpevm.Task {
	tasks := make([]commonpevm.Task, count)
	conflictKey := commonpevm.StorageKey{
		Address: common.BigToAddress(big.NewInt(999)),
		Slot:    common.BigToHash(big.NewInt(1)),
		Type:    commonpevm.KeyTypeStorage,
	}

	for i := 0; i < count; i++ {
		// Create tasks that all access the same storage key
		operations := []Operation{
			{Type: OpRead, Key: conflictKey},
			{Type: OpWrite, Key: conflictKey, Value: i},
		}

		task := &ExampleTask{
			id:           commonpevm.TaskID(i),
			dependencies: []commonpevm.TaskID{},
			gasEstimate:  21000,
			duration:     time.Millisecond * 5,
			sender:       common.BigToAddress(big.NewInt(int64(i))),
			hash:         common.BigToHash(big.NewInt(int64(i))),
			operations:   operations,
		}

		tasks[i] = task
	}

	return tasks
}

func createEVMTransactions(count int) []commonpevm.Task {
	tasks := make([]commonpevm.Task, count)

	for i := 0; i < count; i++ {
		// Simulate different types of EVM transactions
		var gasEstimate uint64
		var operations []Operation

		switch i % 4 {
		case 0: // Simple transfer
			gasEstimate = 21000
			operations = []Operation{
				{Type: OpRead, Key: createAccountKey(i)},
				{Type: OpWrite, Key: createAccountKey(i), Value: big.NewInt(int64(i * 1000))},
			}
		case 1: // Contract call
			gasEstimate = 50000
			operations = []Operation{
				{Type: OpRead, Key: createStorageKey(i, 0)},
				{Type: OpWrite, Key: createStorageKey(i, 0), Value: i},
				{Type: OpWrite, Key: createStorageKey(i, 1), Value: i * 2},
			}
		case 2: // Token transfer
			gasEstimate = 65000
			operations = []Operation{
				{Type: OpRead, Key: createStorageKey(100, i)},   // From balance
				{Type: OpRead, Key: createStorageKey(100, i+1)}, // To balance
				{Type: OpWrite, Key: createStorageKey(100, i), Value: i * 100},
				{Type: OpWrite, Key: createStorageKey(100, i+1), Value: (i + 1) * 100},
			}
		case 3: // Complex contract interaction
			gasEstimate = 150000
			operations = []Operation{
				{Type: OpRead, Key: createStorageKey(i, 0)},
				{Type: OpRead, Key: createStorageKey(i, 1)},
				{Type: OpRead, Key: createStorageKey(i, 2)},
				{Type: OpWrite, Key: createStorageKey(i, 3), Value: i},
				{Type: OpWrite, Key: createStorageKey(i, 4), Value: i * i},
			}
		}

		task := &ExampleTask{
			id:           commonpevm.TaskID(i),
			dependencies: []commonpevm.TaskID{},
			gasEstimate:  gasEstimate,
			duration:     time.Duration(gasEstimate/1000) * time.Microsecond,
			sender:       common.BigToAddress(big.NewInt(int64(i % 20))),
			hash:         common.BigToHash(big.NewInt(int64(i))),
			operations:   operations,
		}

		tasks[i] = task
	}

	return tasks
}

func createOperations(taskID int) []Operation {
	// Create a mix of read and write operations
	operations := []Operation{
		{
			Type: OpRead,
			Key: commonpevm.StorageKey{
				Address: common.BigToAddress(big.NewInt(int64(taskID % 5))),
				Slot:    common.BigToHash(big.NewInt(0)),
				Type:    commonpevm.KeyTypeAccount,
			},
		},
		{
			Type: OpWrite,
			Key: commonpevm.StorageKey{
				Address: common.BigToAddress(big.NewInt(int64(taskID))),
				Slot:    common.BigToHash(big.NewInt(1)),
				Type:    commonpevm.KeyTypeStorage,
			},
			Value: taskID * 100,
		},
	}

	return operations
}

func createAccountKey(address int) commonpevm.StorageKey {
	return commonpevm.StorageKey{
		Address: common.BigToAddress(big.NewInt(int64(address))),
		Slot:    common.Hash{},
		Type:    commonpevm.KeyTypeAccount,
	}
}

func createStorageKey(address, slot int) commonpevm.StorageKey {
	return commonpevm.StorageKey{
		Address: common.BigToAddress(big.NewInt(int64(address))),
		Slot:    common.BigToHash(big.NewInt(int64(slot))),
		Type:    commonpevm.KeyTypeStorage,
	}
}

func benchmarkConfiguration(workers, taskCount int) time.Duration {
	config := engine.DefaultConfig()
	config.MaxWorkers = workers
	config.EnableMetrics = false // Disable for pure performance

	eng := engine.NewEngine(config)
	defer eng.Stop()

	tasks := createSampleTasks(taskCount, false)

	ctx := context.Background()
	options := engine.ExecutionOptions{
		ExecutionOptions: commonpevm.ExecutionOptions{
			MaxWorkers:    workers,
			EnableMetrics: false,
			Timeout:       time.Minute * 5,
		},
	}

	start := time.Now()
	_, err := eng.Execute(ctx, tasks, options)
	if err != nil {
		log.Printf("Benchmark failed: %v", err)
		return time.Hour // Return large duration on error
	}

	return time.Since(start)
}
