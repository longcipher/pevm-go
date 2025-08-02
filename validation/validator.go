// Package validation provides conflict detection and validation for the PEVM engine.
package validation

import (
	"sync"
	"time"

	"github.com/longcipher/pevm-go/common"
	"github.com/longcipher/pevm-go/storage"
)

// Validator defines the interface for execution validation.
type Validator interface {
	// Validate checks if an execution result is valid given the current state.
	Validate(result common.ExecutionResult, stor storage.Storage) common.ValidationResult

	// ValidateBatch validates multiple execution results in batch.
	ValidateBatch(results []common.ExecutionResult, stor storage.Storage) []common.ValidationResult

	// DetectConflicts detects conflicts between execution results.
	DetectConflicts(results []common.ExecutionResult) []ConflictPair

	// GetMetrics returns validation metrics.
	GetMetrics() ValidationMetrics
}

// ConflictPair represents a conflict between two tasks.
type ConflictPair struct {
	Task1       common.TaskID
	Task2       common.TaskID
	ConflictKey common.StorageKey
	Type        common.ConflictType
}

// ValidationMetrics contains metrics about validation performance.
type ValidationMetrics struct {
	TotalValidations      int64         // Total validations performed
	SuccessfulValidations int64         // Validations that passed
	FailedValidations     int64         // Validations that failed
	ConflictsDetected     int64         // Total conflicts detected
	AvgValidationTime     time.Duration // Average validation time
	ConflictsByType       map[common.ConflictType]int64
	BatchValidations      int64   // Number of batch validations
	ValidationThroughput  float64 // Validations per second
}

// FastValidator implements a high-performance validator using optimized algorithms.
type FastValidator struct {
	config           Config
	metrics          ValidationMetrics
	metricsLock      sync.RWMutex
	conflictCache    *ConflictCache
	validationPool   sync.Pool
	lastMetricUpdate time.Time
}

// Config contains configuration for the validator.
type Config struct {
	EnableBatchValidation bool          // Whether to enable batch validation
	ConflictCacheSize     int           // Size of conflict cache
	ValidationTimeout     time.Duration // Timeout for individual validations
	EnableFastPath        bool          // Enable fast path for simple validations
	ConcurrentValidations int           // Number of concurrent validation threads
}

// ConflictCache caches conflict detection results to avoid recomputation.
type ConflictCache struct {
	cache   map[CacheKey]bool
	mutex   sync.RWMutex
	maxSize int
}

// CacheKey represents a key for the conflict cache.
type CacheKey struct {
	Key1 common.StorageKey
	Key2 common.StorageKey
	Op1  OperationType
	Op2  OperationType
}

// OperationType represents the type of operation.
type OperationType uint8

const (
	OperationRead OperationType = iota
	OperationWrite
)

// ValidationContext contains context for a validation operation.
type ValidationContext struct {
	TaskID       common.TaskID
	Reads        []common.ReadDescriptor
	Writes       []common.WriteDescriptor
	Dependencies []common.TaskID
	Timestamp    time.Time
}

// NewFastValidator creates a new fast validator.
func NewFastValidator(config Config) *FastValidator {
	if config.ConflictCacheSize <= 0 {
		config.ConflictCacheSize = DefaultConflictCacheSize
	}
	if config.ConcurrentValidations <= 0 {
		config.ConcurrentValidations = DefaultConcurrentValidations
	}

	validator := &FastValidator{
		config: config,
		conflictCache: &ConflictCache{
			cache:   make(map[CacheKey]bool),
			maxSize: config.ConflictCacheSize,
		},
		metrics: ValidationMetrics{
			ConflictsByType: make(map[common.ConflictType]int64),
		},
	}

	// Initialize validation pool
	validator.validationPool = sync.Pool{
		New: func() interface{} {
			return &ValidationContext{}
		},
	}

	return validator
}

// Validate checks if an execution result is valid given the current state.
func (fv *FastValidator) Validate(result common.ExecutionResult, stor storage.Storage) common.ValidationResult {
	startTime := time.Now()
	defer func() {
		fv.updateMetrics(time.Since(startTime), 1)
	}()

	// Get validation context from pool
	ctx := fv.validationPool.Get().(*ValidationContext)
	defer fv.validationPool.Put(ctx)

	// Reset context
	ctx.TaskID = result.TaskID
	ctx.Reads = result.Reads
	ctx.Writes = result.Writes
	ctx.Timestamp = result.Timestamp

	// Fast path for simple validations
	if fv.config.EnableFastPath && fv.canUseFastPath(ctx) {
		return fv.validateFastPath(ctx, stor)
	}

	// Full validation
	return fv.validateFull(ctx, stor)
}

// ValidateBatch validates multiple execution results in batch.
func (fv *FastValidator) ValidateBatch(results []common.ExecutionResult, stor storage.Storage) []common.ValidationResult {
	if !fv.config.EnableBatchValidation {
		// Fall back to individual validations
		validationResults := make([]common.ValidationResult, len(results))
		for i, result := range results {
			validationResults[i] = fv.Validate(result, stor)
		}
		return validationResults
	}

	startTime := time.Now()
	defer func() {
		fv.updateMetrics(time.Since(startTime), len(results))
		fv.metricsLock.Lock()
		fv.metrics.BatchValidations++
		fv.metricsLock.Unlock()
	}()

	return fv.validateBatch(results, stor)
}

// DetectConflicts detects conflicts between execution results.
func (fv *FastValidator) DetectConflicts(results []common.ExecutionResult) []ConflictPair {
	conflicts := make([]ConflictPair, 0)

	// Build access maps for efficient conflict detection
	readMap := make(map[common.StorageKey][]common.TaskID)
	writeMap := make(map[common.StorageKey][]common.TaskID)

	for _, result := range results {
		for _, read := range result.Reads {
			readMap[read.Key] = append(readMap[read.Key], result.TaskID)
		}
		for _, write := range result.Writes {
			writeMap[write.Key] = append(writeMap[write.Key], result.TaskID)
		}
	}

	// Detect read-write conflicts
	for key, writers := range writeMap {
		if readers, exists := readMap[key]; exists {
			for _, writer := range writers {
				for _, reader := range readers {
					if writer != reader && fv.hasConflict(writer, reader, key) {
						conflicts = append(conflicts, ConflictPair{
							Task1:       writer,
							Task2:       reader,
							ConflictKey: key,
							Type:        common.ConflictTypeReadWrite,
						})
					}
				}
			}
		}
	}

	// Detect write-write conflicts
	for key, writers := range writeMap {
		for i := 0; i < len(writers); i++ {
			for j := i + 1; j < len(writers); j++ {
				if fv.hasConflict(writers[i], writers[j], key) {
					conflicts = append(conflicts, ConflictPair{
						Task1:       writers[i],
						Task2:       writers[j],
						ConflictKey: key,
						Type:        common.ConflictTypeWriteWrite,
					})
				}
			}
		}
	}

	fv.metricsLock.Lock()
	fv.metrics.ConflictsDetected += int64(len(conflicts))
	for _, conflict := range conflicts {
		fv.metrics.ConflictsByType[conflict.Type]++
	}
	fv.metricsLock.Unlock()

	return conflicts
}

// canUseFastPath determines if fast path validation can be used.
func (fv *FastValidator) canUseFastPath(ctx *ValidationContext) bool {
	// Fast path criteria:
	// 1. Small number of reads/writes
	// 2. No complex dependencies
	// 3. Simple key patterns

	if len(ctx.Reads) > 10 || len(ctx.Writes) > 5 {
		return false
	}

	if len(ctx.Dependencies) > 3 {
		return false
	}

	return true
}

// validateFastPath performs fast path validation.
func (fv *FastValidator) validateFastPath(ctx *ValidationContext, stor storage.Storage) common.ValidationResult {
	// Simple validation: check if all reads are still valid
	for _, read := range ctx.Reads {
		currentResult := stor.Read(read.Key, int(ctx.TaskID))
		if !fv.isReadStillValid(read, currentResult) {
			return common.ValidationResult{
				TaskID: ctx.TaskID,
				Valid:  false,
				Conflict: &common.Conflict{
					ConflictedKey: read.Key,
					ConflictType:  common.ConflictTypeReadWrite,
				},
			}
		}
	}

	fv.metricsLock.Lock()
	fv.metrics.SuccessfulValidations++
	fv.metricsLock.Unlock()

	return common.ValidationResult{
		TaskID: ctx.TaskID,
		Valid:  true,
	}
}

// validateFull performs full validation with all checks.
func (fv *FastValidator) validateFull(ctx *ValidationContext, stor storage.Storage) common.ValidationResult {
	// Check read validity
	for _, read := range ctx.Reads {
		currentResult := stor.Read(read.Key, int(ctx.TaskID))
		if !fv.isReadStillValid(read, currentResult) {
			fv.metricsLock.Lock()
			fv.metrics.FailedValidations++
			fv.metricsLock.Unlock()

			return common.ValidationResult{
				TaskID: ctx.TaskID,
				Valid:  false,
				Conflict: &common.Conflict{
					ConflictedKey: read.Key,
					ConflictType:  common.ConflictTypeReadWrite,
				},
			}
		}
	}

	// Check write consistency
	for _, write := range ctx.Writes {
		if !fv.isWriteValid(write, stor) {
			fv.metricsLock.Lock()
			fv.metrics.FailedValidations++
			fv.metricsLock.Unlock()

			return common.ValidationResult{
				TaskID: ctx.TaskID,
				Valid:  false,
				Conflict: &common.Conflict{
					ConflictedKey: write.Key,
					ConflictType:  common.ConflictTypeWriteWrite,
				},
			}
		}
	}

	// Check dependency constraints
	for _, depID := range ctx.Dependencies {
		if !fv.isDependencyValid(depID, ctx.TaskID) {
			fv.metricsLock.Lock()
			fv.metrics.FailedValidations++
			fv.metricsLock.Unlock()

			return common.ValidationResult{
				TaskID: ctx.TaskID,
				Valid:  false,
				Conflict: &common.Conflict{
					ConflictingTaskID: depID,
					ConflictType:      common.ConflictTypeDependency,
				},
			}
		}
	}

	fv.metricsLock.Lock()
	fv.metrics.SuccessfulValidations++
	fv.metricsLock.Unlock()

	return common.ValidationResult{
		TaskID: ctx.TaskID,
		Valid:  true,
	}
}

// validateBatch performs batch validation for multiple results.
func (fv *FastValidator) validateBatch(results []common.ExecutionResult, stor storage.Storage) []common.ValidationResult {
	validationResults := make([]common.ValidationResult, len(results))

	// Create worker goroutines for parallel validation
	numWorkers := fv.config.ConcurrentValidations
	if numWorkers > len(results) {
		numWorkers = len(results)
	}

	resultChan := make(chan struct {
		index  int
		result common.ValidationResult
	}, len(results))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := workerID; j < len(results); j += numWorkers {
				result := fv.Validate(results[j], stor)
				resultChan <- struct {
					index  int
					result common.ValidationResult
				}{j, result}
			}
		}(i)
	}

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	for item := range resultChan {
		validationResults[item.index] = item.result
	}

	return validationResults
}

// isReadStillValid checks if a read is still valid.
func (fv *FastValidator) isReadStillValid(original common.ReadDescriptor, current storage.ReadResult) bool {
	switch current.Status {
	case storage.ReadStatusFound:
		return original.Version.Compare(current.Version) == 0
	case storage.ReadStatusNotFound:
		return original.Value == nil
	case storage.ReadStatusDependency, storage.ReadStatusEstimate:
		return current.Dependency == original.Version.TxID
	default:
		return false
	}
}

// isWriteValid checks if a write is valid.
func (fv *FastValidator) isWriteValid(write common.WriteDescriptor, stor storage.Storage) bool {
	// Check if there are any conflicting writes
	result := stor.Read(write.Key, int(write.Version.TxID))
	return result.Status == storage.ReadStatusNotFound ||
		result.Version.TxID < write.Version.TxID
}

// isDependencyValid checks if a dependency constraint is satisfied.
func (fv *FastValidator) isDependencyValid(depID common.TaskID, taskID common.TaskID) bool {
	// Check if dependency task has completed before this task
	return int(depID) < int(taskID)
}

// hasConflict checks if two tasks have a conflict on a specific key.
func (fv *FastValidator) hasConflict(task1, task2 common.TaskID, key common.StorageKey) bool {
	// Check conflict cache first
	cacheKey := CacheKey{
		Key1: key,
		Key2: key,
		Op1:  OperationWrite, // Simplified for this example
		Op2:  OperationRead,
	}

	fv.conflictCache.mutex.RLock()
	if conflict, exists := fv.conflictCache.cache[cacheKey]; exists {
		fv.conflictCache.mutex.RUnlock()
		return conflict
	}
	fv.conflictCache.mutex.RUnlock()

	// Compute conflict
	hasConflict := int(task1) != int(task2) // Simplified conflict detection

	// Cache the result
	fv.conflictCache.mutex.Lock()
	if len(fv.conflictCache.cache) >= fv.conflictCache.maxSize {
		// Evict random entry to make space
		for k := range fv.conflictCache.cache {
			delete(fv.conflictCache.cache, k)
			break
		}
	}
	fv.conflictCache.cache[cacheKey] = hasConflict
	fv.conflictCache.mutex.Unlock()

	return hasConflict
}

// updateMetrics updates validation metrics.
func (fv *FastValidator) updateMetrics(duration time.Duration, count int) {
	fv.metricsLock.Lock()
	defer fv.metricsLock.Unlock()

	fv.metrics.TotalValidations += int64(count)

	// Update average validation time
	if fv.metrics.TotalValidations > 0 {
		totalTime := time.Duration(fv.metrics.AvgValidationTime) * time.Duration(fv.metrics.TotalValidations-int64(count))
		totalTime += duration
		fv.metrics.AvgValidationTime = totalTime / time.Duration(fv.metrics.TotalValidations)
	} else {
		fv.metrics.AvgValidationTime = duration
	}

	// Update throughput
	now := time.Now()
	if !fv.lastMetricUpdate.IsZero() {
		elapsed := now.Sub(fv.lastMetricUpdate).Seconds()
		if elapsed > 0 {
			fv.metrics.ValidationThroughput = float64(count) / elapsed
		}
	}
	fv.lastMetricUpdate = now
}

// GetMetrics returns validation metrics.
func (fv *FastValidator) GetMetrics() ValidationMetrics {
	fv.metricsLock.RLock()
	defer fv.metricsLock.RUnlock()

	// Create a copy of the metrics
	metrics := fv.metrics
	metrics.ConflictsByType = make(map[common.ConflictType]int64)
	for k, v := range fv.metrics.ConflictsByType {
		metrics.ConflictsByType[k] = v
	}

	return metrics
}

// EarlyValidator performs validation as soon as execution completes.
type EarlyValidator struct {
	*FastValidator
	validationQueue chan ValidationRequest
	workers         []*ValidationWorker
	wg              sync.WaitGroup
	shutdown        chan struct{}
}

// ValidationRequest represents a validation request.
type ValidationRequest struct {
	Result   common.ExecutionResult
	Storage  storage.Storage
	Response chan common.ValidationResult
}

// ValidationWorker processes validation requests.
type ValidationWorker struct {
	id        int
	validator *FastValidator
	requests  <-chan ValidationRequest
}

// NewEarlyValidator creates a new early validator.
func NewEarlyValidator(config Config) *EarlyValidator {
	fastValidator := NewFastValidator(config)

	ev := &EarlyValidator{
		FastValidator:   fastValidator,
		validationQueue: make(chan ValidationRequest, config.ConcurrentValidations*10),
		workers:         make([]*ValidationWorker, config.ConcurrentValidations),
		shutdown:        make(chan struct{}),
	}

	// Start validation workers
	for i := 0; i < config.ConcurrentValidations; i++ {
		worker := &ValidationWorker{
			id:        i,
			validator: fastValidator,
			requests:  ev.validationQueue,
		}
		ev.workers[i] = worker

		ev.wg.Add(1)
		go ev.workerLoop(worker)
	}

	return ev
}

// workerLoop runs the validation worker loop.
func (ev *EarlyValidator) workerLoop(worker *ValidationWorker) {
	defer ev.wg.Done()

	for {
		select {
		case <-ev.shutdown:
			return
		case request := <-worker.requests:
			result := worker.validator.Validate(request.Result, request.Storage)
			request.Response <- result
		}
	}
}

// ValidateAsync performs asynchronous validation.
func (ev *EarlyValidator) ValidateAsync(result common.ExecutionResult, stor storage.Storage) <-chan common.ValidationResult {
	response := make(chan common.ValidationResult, 1)

	request := ValidationRequest{
		Result:   result,
		Storage:  stor,
		Response: response,
	}

	select {
	case ev.validationQueue <- request:
		return response
	default:
		// Queue is full, perform synchronous validation
		go func() {
			result := ev.FastValidator.Validate(result, stor)
			response <- result
		}()
		return response
	}
}

// Shutdown gracefully shuts down the early validator.
func (ev *EarlyValidator) Shutdown() {
	close(ev.shutdown)
	ev.wg.Wait()
}

// Default configuration values.
const (
	DefaultConflictCacheSize     = 10000
	DefaultConcurrentValidations = 4
)
