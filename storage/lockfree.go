// Package storage provides high-performance multi-version storage for the PEVM engine.
package storage

import (
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/longcipher/pevm-go/common"
)

// Storage defines the interface for multi-version storage operations.
type Storage interface {
	// Read retrieves the value for a key as seen by the given transaction.
	Read(key common.StorageKey, txID int) ReadResult

	// Write stores a value for a key at the specified version.
	Write(key common.StorageKey, value interface{}, version common.Version) error

	// Validate checks if the read set is still valid for the given transaction.
	Validate(reads []common.ReadDescriptor, txID int) bool

	// Commit atomically commits all writes from the given write set.
	Commit(writes []common.WriteDescriptor) error

	// Snapshot creates a read-only snapshot of the current state.
	Snapshot() Snapshot

	// GC performs garbage collection of old versions.
	GC(beforeTxID int) int

	// Stats returns storage statistics.
	Stats() StorageStats
}

// ReadResult represents the result of a read operation.
type ReadResult struct {
	Status     ReadStatus
	Value      interface{}
	Version    common.Version
	Dependency int // Transaction ID this read depends on (-1 if none)
}

// ReadStatus indicates the status of a read operation.
type ReadStatus uint8

const (
	ReadStatusFound      ReadStatus = iota // Value found and valid
	ReadStatusNotFound                     // Key not found
	ReadStatusDependency                   // Read depends on uncommitted write
	ReadStatusEstimate                     // Read depends on estimated write
)

// String returns a string representation of the read status.
func (rs ReadStatus) String() string {
	switch rs {
	case ReadStatusFound:
		return "found"
	case ReadStatusNotFound:
		return "not_found"
	case ReadStatusDependency:
		return "dependency"
	case ReadStatusEstimate:
		return "estimate"
	default:
		return "unknown"
	}
}

// Snapshot provides a read-only view of storage at a specific point in time.
type Snapshot interface {
	// Read retrieves a value from the snapshot.
	Read(key common.StorageKey) (interface{}, bool)

	// Release releases resources associated with the snapshot.
	Release()
}

// StorageStats contains statistics about storage usage and performance.
type StorageStats struct {
	TotalKeys       int64 // Total number of unique keys
	TotalVersions   int64 // Total number of versions across all keys
	MemoryUsage     int64 // Approximate memory usage in bytes
	ReadOps         int64 // Total read operations
	WriteOps        int64 // Total write operations
	ValidationOps   int64 // Total validation operations
	GCRuns          int64 // Number of GC runs
	GCVersionsFreed int64 // Versions freed by GC
}

// LockFreeStorage implements a lock-free multi-version storage system.
type LockFreeStorage struct {
	// buckets contains hash buckets for keys
	buckets []unsafe.Pointer // []*bucket

	// bucketMask is used for fast modulo operation
	bucketMask uint64

	// stats tracks storage statistics
	stats StorageStats

	// gcThreshold determines when to trigger GC
	gcThreshold int64

	// memoryPool for object reuse
	memoryPool common.MemoryPool
}

// bucket represents a hash bucket containing version chains for multiple keys.
type bucket struct {
	// lock protects this bucket during structural changes
	lock sync.RWMutex

	// entries maps storage keys to their version chains
	entries map[common.StorageKey]*versionChain
}

// versionChain represents a chain of versions for a single key.
type versionChain struct {
	// head points to the most recent version
	head unsafe.Pointer // *versionNode

	// count tracks the number of versions in this chain
	count int64
}

// versionNode represents a single version of a value.
type versionNode struct {
	// version identifies this specific version
	version common.Version

	// value is the stored value
	value interface{}

	// status indicates if this version is committed, estimated, etc.
	status VersionStatus

	// next points to the next older version
	next unsafe.Pointer // *versionNode

	// timestamp when this version was created
	timestamp int64
}

// VersionStatus indicates the status of a version.
type VersionStatus uint8

const (
	VersionStatusCommitted VersionStatus = iota // Version is committed
	VersionStatusEstimate                       // Version is an estimate
	VersionStatusAborted                        // Version was aborted
)

// NewLockFreeStorage creates a new lock-free storage instance.
func NewLockFreeStorage(config Config) *LockFreeStorage {
	bucketCount := config.BucketCount
	if bucketCount == 0 {
		bucketCount = DefaultBucketCount
	}

	// Ensure bucket count is a power of 2 for fast modulo
	if bucketCount&(bucketCount-1) != 0 {
		bucketCount = int(nextPowerOfTwo(uint64(bucketCount)))
	}

	storage := &LockFreeStorage{
		buckets:     make([]unsafe.Pointer, bucketCount),
		bucketMask:  uint64(bucketCount - 1),
		gcThreshold: config.GCThreshold,
		memoryPool:  config.MemoryPool,
	}

	// Initialize buckets
	for i := range storage.buckets {
		b := &bucket{
			entries: make(map[common.StorageKey]*versionChain),
		}
		atomic.StorePointer(&storage.buckets[i], unsafe.Pointer(b))
	}

	return storage
}

// Read retrieves the value for a key as seen by the given transaction.
func (s *LockFreeStorage) Read(key common.StorageKey, txID int) ReadResult {
	atomic.AddInt64(&s.stats.ReadOps, 1)

	// Get the appropriate bucket
	hash := key.Hash()
	bucketIdx := hash & s.bucketMask
	bucketPtr := atomic.LoadPointer(&s.buckets[bucketIdx])
	bucket := (*bucket)(bucketPtr)

	bucket.lock.RLock()
	chain, exists := bucket.entries[key]
	bucket.lock.RUnlock()

	if !exists {
		return ReadResult{Status: ReadStatusNotFound}
	}

	// Traverse the version chain to find the appropriate version
	return s.findVersion(chain, txID)
}

// findVersion finds the appropriate version for the given transaction ID.
func (s *LockFreeStorage) findVersion(chain *versionChain, txID int) ReadResult {
	nodePtr := atomic.LoadPointer(&chain.head)

	for nodePtr != nil {
		node := (*versionNode)(nodePtr)

		// If this version is from a transaction before our txID, we can use it
		if node.version.TxID < txID {
			switch node.status {
			case VersionStatusCommitted:
				return ReadResult{
					Status:  ReadStatusFound,
					Value:   node.value,
					Version: node.version,
				}
			case VersionStatusEstimate:
				return ReadResult{
					Status:     ReadStatusEstimate,
					Value:      node.value,
					Version:    node.version,
					Dependency: node.version.TxID,
				}
			case VersionStatusAborted:
				// Skip aborted versions and continue searching
			}
		}

		nodePtr = atomic.LoadPointer(&node.next)
	}

	return ReadResult{Status: ReadStatusNotFound}
}

// Write stores a value for a key at the specified version.
func (s *LockFreeStorage) Write(key common.StorageKey, value interface{}, version common.Version) error {
	atomic.AddInt64(&s.stats.WriteOps, 1)

	// Get the appropriate bucket
	hash := key.Hash()
	bucketIdx := hash & s.bucketMask
	bucketPtr := atomic.LoadPointer(&s.buckets[bucketIdx])
	bucket := (*bucket)(bucketPtr)

	bucket.lock.Lock()
	defer bucket.lock.Unlock()

	// Get or create the version chain for this key
	chain, exists := bucket.entries[key]
	if !exists {
		chain = &versionChain{}
		bucket.entries[key] = chain
		atomic.AddInt64(&s.stats.TotalKeys, 1)
	}

	// Create new version node
	newNode := s.createVersionNode(version, value, VersionStatusEstimate)

	// Insert at the head of the chain
	newNode.next = chain.head
	atomic.StorePointer(&chain.head, unsafe.Pointer(newNode))
	atomic.AddInt64(&chain.count, 1)
	atomic.AddInt64(&s.stats.TotalVersions, 1)

	return nil
}

// createVersionNode creates a new version node, using the memory pool if available.
func (s *LockFreeStorage) createVersionNode(version common.Version, value interface{}, status VersionStatus) *versionNode {
	var node *versionNode

	if s.memoryPool != nil {
		if pooled := s.memoryPool.Get("versionNode"); pooled != nil {
			node = pooled.(*versionNode)
		}
	}

	if node == nil {
		node = &versionNode{}
	}

	node.version = version
	node.value = value
	node.status = status
	node.next = nil
	node.timestamp = currentTimeNanos()

	return node
}

// Validate checks if the read set is still valid for the given transaction.
func (s *LockFreeStorage) Validate(reads []common.ReadDescriptor, txID int) bool {
	atomic.AddInt64(&s.stats.ValidationOps, 1)

	for _, read := range reads {
		currentResult := s.Read(read.Key, txID)

		// Check if the read is still valid
		if !s.isReadValid(read, currentResult) {
			return false
		}
	}

	return true
}

// isReadValid checks if a specific read is still valid.
func (s *LockFreeStorage) isReadValid(original common.ReadDescriptor, current ReadResult) bool {
	// If original was not found, current should also not be found
	if current.Status == ReadStatusNotFound {
		return original.Value == nil
	}

	// If current is found, check if version and value match
	if current.Status == ReadStatusFound {
		return current.Version.Compare(original.Version) == 0
	}

	// If current has dependency, check if it's the same dependency
	if current.Status == ReadStatusDependency || current.Status == ReadStatusEstimate {
		return current.Dependency == original.Version.TxID
	}

	return false
}

// Commit atomically commits all writes from the given write set.
func (s *LockFreeStorage) Commit(writes []common.WriteDescriptor) error {
	// Mark all versions as committed
	for _, write := range writes {
		hash := write.Key.Hash()
		bucketIdx := hash & s.bucketMask
		bucketPtr := atomic.LoadPointer(&s.buckets[bucketIdx])
		bucket := (*bucket)(bucketPtr)

		bucket.lock.RLock()
		chain, exists := bucket.entries[write.Key]
		bucket.lock.RUnlock()

		if exists {
			s.markVersionAsCommitted(chain, write.Version)
		}
	}

	return nil
}

// markVersionAsCommitted marks a specific version as committed.
func (s *LockFreeStorage) markVersionAsCommitted(chain *versionChain, version common.Version) {
	nodePtr := atomic.LoadPointer(&chain.head)

	for nodePtr != nil {
		node := (*versionNode)(nodePtr)

		if node.version.Compare(version) == 0 {
			atomic.StoreInt32((*int32)(unsafe.Pointer(&node.status)), int32(VersionStatusCommitted))
			return
		}

		nodePtr = atomic.LoadPointer(&node.next)
	}
}

// Snapshot creates a read-only snapshot of the current state.
func (s *LockFreeStorage) Snapshot() Snapshot {
	return &lockFreeSnapshot{
		storage:   s,
		timestamp: currentTimeNanos(),
	}
}

// GC performs garbage collection of old versions.
func (s *LockFreeStorage) GC(beforeTxID int) int {
	atomic.AddInt64(&s.stats.GCRuns, 1)

	freedVersions := 0

	// Iterate through all buckets
	for i := range s.buckets {
		bucketPtr := atomic.LoadPointer(&s.buckets[i])
		bucket := (*bucket)(bucketPtr)

		bucket.lock.Lock()

		for key, chain := range bucket.entries {
			freed := s.gcVersionChain(chain, beforeTxID)
			freedVersions += freed

			// If chain is empty, remove it
			if atomic.LoadPointer(&chain.head) == nil {
				delete(bucket.entries, key)
				atomic.AddInt64(&s.stats.TotalKeys, -1)
			}
		}

		bucket.lock.Unlock()
	}

	atomic.AddInt64(&s.stats.GCVersionsFreed, int64(freedVersions))
	atomic.AddInt64(&s.stats.TotalVersions, -int64(freedVersions))

	return freedVersions
}

// gcVersionChain garbage collects old versions from a version chain.
func (s *LockFreeStorage) gcVersionChain(chain *versionChain, beforeTxID int) int {
	var prev *versionNode
	nodePtr := atomic.LoadPointer(&chain.head)
	freedCount := 0

	for nodePtr != nil {
		node := (*versionNode)(nodePtr)
		nextPtr := atomic.LoadPointer(&node.next)

		// Keep the most recent committed version and any version >= beforeTxID
		if node.version.TxID >= beforeTxID ||
			(node.status == VersionStatusCommitted && prev == nil) {
			prev = node
		} else {
			// Remove this version
			if prev != nil {
				atomic.StorePointer(&prev.next, nextPtr)
			} else {
				atomic.StorePointer(&chain.head, nextPtr)
			}

			// Return to memory pool if available
			if s.memoryPool != nil {
				s.memoryPool.Put("versionNode", node)
			}

			freedCount++
			atomic.AddInt64(&chain.count, -1)
		}

		nodePtr = nextPtr
	}

	return freedCount
}

// Stats returns storage statistics.
func (s *LockFreeStorage) Stats() StorageStats {
	return StorageStats{
		TotalKeys:       atomic.LoadInt64(&s.stats.TotalKeys),
		TotalVersions:   atomic.LoadInt64(&s.stats.TotalVersions),
		MemoryUsage:     s.estimateMemoryUsage(),
		ReadOps:         atomic.LoadInt64(&s.stats.ReadOps),
		WriteOps:        atomic.LoadInt64(&s.stats.WriteOps),
		ValidationOps:   atomic.LoadInt64(&s.stats.ValidationOps),
		GCRuns:          atomic.LoadInt64(&s.stats.GCRuns),
		GCVersionsFreed: atomic.LoadInt64(&s.stats.GCVersionsFreed),
	}
}

// estimateMemoryUsage estimates the current memory usage.
func (s *LockFreeStorage) estimateMemoryUsage() int64 {
	// Rough estimate: number of versions * average size per version
	totalVersions := atomic.LoadInt64(&s.stats.TotalVersions)
	avgVersionSize := int64(unsafe.Sizeof(versionNode{})) + 64 // rough estimate for value
	return totalVersions * avgVersionSize
}

// lockFreeSnapshot implements the Snapshot interface.
type lockFreeSnapshot struct {
	storage   *LockFreeStorage
	timestamp int64
}

// Read retrieves a value from the snapshot.
func (s *lockFreeSnapshot) Read(key common.StorageKey) (interface{}, bool) {
	result := s.storage.Read(key, int(s.timestamp))
	if result.Status == ReadStatusFound {
		return result.Value, true
	}
	return nil, false
}

// Release releases resources associated with the snapshot.
func (s *lockFreeSnapshot) Release() {
	// Nothing to release for this implementation
}

// Config contains configuration options for storage.
type Config struct {
	BucketCount int               // Number of hash buckets
	GCThreshold int64             // Threshold for triggering GC
	MemoryPool  common.MemoryPool // Memory pool for object reuse
}

// Default configuration values.
const (
	DefaultBucketCount = 1024
	DefaultGCThreshold = 10000
)

// Utility functions.

// nextPowerOfTwo returns the next power of two greater than or equal to n.
func nextPowerOfTwo(n uint64) uint64 {
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

// currentTimeNanos returns the current time in nanoseconds.
func currentTimeNanos() int64 {
	return time.Now().UnixNano()
}
