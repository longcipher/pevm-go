// Package common provides shared types and utilities for the PEVM parallel execution engine.
package common

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Version represents a specific version of a storage key in the multi-version system.
type Version struct {
	TxID        int // Transaction ID
	Incarnation int // Incarnation number (for re-executed transactions)
}

// String returns a string representation of the version.
func (v Version) String() string {
	return fmt.Sprintf("V(%d:%d)", v.TxID, v.Incarnation)
}

// Compare compares two versions. Returns -1 if v < other, 0 if v == other, 1 if v > other.
func (v Version) Compare(other Version) int {
	if v.TxID < other.TxID {
		return -1
	}
	if v.TxID > other.TxID {
		return 1
	}
	if v.Incarnation < other.Incarnation {
		return -1
	}
	if v.Incarnation > other.Incarnation {
		return 1
	}
	return 0
}

// StorageKey represents a key in the storage system.
type StorageKey struct {
	Address common.Address // Contract address
	Slot    common.Hash    // Storage slot
	Type    KeyType        // Type of key (account, storage, code, etc.)
}

// KeyType represents the type of storage key.
type KeyType uint8

const (
	KeyTypeAccount KeyType = iota // Account balance/nonce
	KeyTypeStorage                // Contract storage slot
	KeyTypeCode                   // Contract code
	KeyTypeSubpath                // EVM subpath (logs, etc.)
)

// String returns a string representation of the key type.
func (kt KeyType) String() string {
	switch kt {
	case KeyTypeAccount:
		return "account"
	case KeyTypeStorage:
		return "storage"
	case KeyTypeCode:
		return "code"
	case KeyTypeSubpath:
		return "subpath"
	default:
		return "unknown"
	}
}

// Hash returns a hash of the storage key for use in hash maps.
func (sk StorageKey) Hash() uint64 {
	hasher := fnv.New64a()
	hasher.Write(sk.Address.Bytes())
	hasher.Write(sk.Slot.Bytes())
	hasher.Write([]byte{byte(sk.Type)})
	return hasher.Sum64()
}

// Equal checks if two storage keys are equal.
func (sk StorageKey) Equal(other StorageKey) bool {
	return sk.Address == other.Address && sk.Slot == other.Slot && sk.Type == other.Type
}

// ReadDescriptor describes a read operation performed by a transaction.
type ReadDescriptor struct {
	Key     StorageKey  // Key that was read
	Version Version     // Version that was read
	Value   interface{} // Value that was read (optional)
}

// WriteDescriptor describes a write operation performed by a transaction.
type WriteDescriptor struct {
	Key     StorageKey  // Key being written
	Version Version     // Version being written
	Value   interface{} // New value
}

// TaskID represents a unique identifier for a task.
type TaskID int

// Task represents a unit of work to be executed in parallel.
type Task interface {
	// ID returns the unique identifier for this task.
	ID() TaskID

	// Dependencies returns the list of task IDs that this task depends on.
	Dependencies() []TaskID

	// EstimatedGas returns an estimate of the gas this task will consume.
	EstimatedGas() uint64

	// EstimatedDuration returns an estimate of how long this task will take to execute.
	EstimatedDuration() time.Duration

	// Sender returns the sender address for ordering purposes.
	Sender() common.Address

	// Hash returns a unique hash for this task.
	Hash() common.Hash
}

// ExecutionResult represents the result of executing a task.
type ExecutionResult struct {
	TaskID    TaskID            // ID of the executed task
	Success   bool              // Whether execution was successful
	Error     error             // Error if execution failed
	GasUsed   uint64            // Amount of gas consumed
	Output    []byte            // Execution output
	Reads     []ReadDescriptor  // All read operations
	Writes    []WriteDescriptor // All write operations
	Duration  time.Duration     // Execution duration
	WorkerID  int               // ID of worker that executed this task
	Timestamp time.Time         // When execution completed
}

// ValidationResult represents the result of validating an execution.
type ValidationResult struct {
	TaskID   TaskID    // ID of the validated task
	Valid    bool      // Whether the execution is valid
	Conflict *Conflict // Conflict information if validation failed
}

// Conflict represents a conflict between two tasks.
type Conflict struct {
	ConflictingTaskID TaskID     // ID of the task that caused the conflict
	ConflictedKey     StorageKey // Key that was conflicted
	ConflictType      ConflictType
}

// ConflictType represents the type of conflict.
type ConflictType uint8

const (
	ConflictTypeReadWrite  ConflictType = iota // Read-write conflict
	ConflictTypeWriteWrite                     // Write-write conflict
	ConflictTypeDependency                     // Dependency violation
)

// String returns a string representation of the conflict type.
func (ct ConflictType) String() string {
	switch ct {
	case ConflictTypeReadWrite:
		return "read-write"
	case ConflictTypeWriteWrite:
		return "write-write"
	case ConflictTypeDependency:
		return "dependency"
	default:
		return "unknown"
	}
}

// ExecutionOptions contains options for parallel execution.
type ExecutionOptions struct {
	MaxWorkers      int                    // Maximum number of worker threads
	EnableMetrics   bool                   // Whether to collect detailed metrics
	Timeout         time.Duration          // Maximum execution time
	Context         context.Context        // Execution context
	WorkerAffinity  bool                   // Whether to pin workers to CPU cores
	MemoryLimit     int64                  // Maximum memory usage
	GCPressureLimit float64                // GC pressure threshold (0.0-1.0)
	ValidationMode  ValidationMode         // Validation mode
	RetryPolicy     RetryPolicy            // Retry policy for failed tasks
	CustomConfig    map[string]interface{} // Custom configuration options
}

// ValidationMode specifies when to perform validation.
type ValidationMode uint8

const (
	ValidationModeEager ValidationMode = iota // Validate immediately after execution
	ValidationModeLazy                        // Validate only when necessary
	ValidationModeBatch                       // Validate in batches
)

// RetryPolicy specifies how to handle failed tasks.
type RetryPolicy struct {
	MaxRetries    int           // Maximum number of retries
	RetryDelay    time.Duration // Delay between retries
	ExponentialBO bool          // Whether to use exponential backoff
}

// WorkerStatus represents the status of a worker.
type WorkerStatus uint8

const (
	WorkerStatusIdle WorkerStatus = iota
	WorkerStatusExecuting
	WorkerStatusValidating
	WorkerStatusBlocked
)

// String returns a string representation of the worker status.
func (ws WorkerStatus) String() string {
	switch ws {
	case WorkerStatusIdle:
		return "idle"
	case WorkerStatusExecuting:
		return "executing"
	case WorkerStatusValidating:
		return "validating"
	case WorkerStatusBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// EngineStatus represents the overall status of the execution engine.
type EngineStatus struct {
	Running        bool                   // Whether the engine is running
	TotalTasks     int                    // Total number of tasks
	CompletedTasks int                    // Number of completed tasks
	FailedTasks    int                    // Number of failed tasks
	ActiveWorkers  int                    // Number of active workers
	QueueDepth     int                    // Number of tasks in queue
	WorkerStatuses map[int]WorkerStatus   // Status of each worker
	MemoryUsage    int64                  // Current memory usage
	StartTime      time.Time              // When execution started
	Metrics        map[string]interface{} // Additional metrics
}

// Logger defines the interface for logging within the PEVM engine.
type Logger interface {
	Debug(msg string, keyvals ...interface{})
	Info(msg string, keyvals ...interface{})
	Warn(msg string, keyvals ...interface{})
	Error(msg string, keyvals ...interface{})
}

// MemoryPool manages object pooling to reduce garbage collection pressure.
type MemoryPool interface {
	// Get retrieves an object from the pool.
	Get(objType string) interface{}

	// Put returns an object to the pool.
	Put(objType string, obj interface{})

	// Stats returns pool statistics.
	Stats() PoolStats
}

// PoolStats contains statistics about memory pool usage.
type PoolStats struct {
	Allocations   int64 // Total allocations
	Deallocations int64 // Total deallocations
	PoolHits      int64 // Number of pool hits
	PoolMisses    int64 // Number of pool misses
	CurrentSize   int64 // Current pool size
	MaxSize       int64 // Maximum pool size
}

// Error types for the PEVM engine.
var (
	ErrTaskNotFound       = errors.New("task not found")
	ErrInvalidDependency  = errors.New("invalid dependency")
	ErrExecutionTimeout   = errors.New("execution timeout")
	ErrValidationFailed   = errors.New("validation failed")
	ErrInsufficientMemory = errors.New("insufficient memory")
	ErrWorkerPanic        = errors.New("worker panic")
	ErrEngineShutdown     = errors.New("engine shutdown")
	ErrCircularDependency = errors.New("circular dependency detected")
)

// TaskError represents an error that occurred during task execution.
type TaskError struct {
	TaskID TaskID
	Err    error
	Retry  bool // Whether the task should be retried
}

// Error implements the error interface.
func (te TaskError) Error() string {
	return fmt.Sprintf("task %d: %v", te.TaskID, te.Err)
}

// Unwrap returns the underlying error.
func (te TaskError) Unwrap() error {
	return te.Err
}

// Constants for performance tuning.
const (
	DefaultWorkerCount         = 8
	DefaultQueueSize           = 1024
	DefaultMemoryPoolSize      = 100 * 1024 * 1024 // 100MB
	DefaultGCPressureLimit     = 0.8
	DefaultValidationBatchSize = 100
	DefaultRetryDelay          = time.Millisecond * 100
	DefaultExecutionTimeout    = time.Minute * 5
)
