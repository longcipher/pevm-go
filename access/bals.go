// Package access provides block-level access list (BAL) primitives and planning utilities.
package access

import (
	gethcommon "github.com/ethereum/go-ethereum/common"
	pevm "github.com/longcipher/pevm-go/common"
)

// Tuple represents an address with a set of storage keys to be accessed.
type Tuple struct {
	Address     gethcommon.Address
	StorageKeys []gethcommon.Hash
}

// List is a block-level access list per task/transaction.
// It maps a task ID to a set of access tuples it intends to touch.
type List map[pevm.TaskID][]Tuple

// AccessOracle abstracts how we obtain access sets for tasks.
// Implementations may use BALs, static hints, or dynamic predictors.
type AccessOracle interface {
	// KeysFor returns the concrete storage keys a task is expected to touch.
	KeysFor(task pevm.Task) []pevm.StorageKey
}

// StaticOracle is a simple oracle backed by a precomputed BAL mapping.
type StaticOracle struct {
	bal     List
	keyType pevm.KeyType
}

// NewStaticOracle creates a new StaticOracle.
// keyType indicates what StorageKey.Type to assign for returned keys (e.g., storage/account).
func NewStaticOracle(bal List, keyType pevm.KeyType) *StaticOracle {
	return &StaticOracle{bal: bal, keyType: keyType}
}

// KeysFor implements AccessOracle.
func (o *StaticOracle) KeysFor(task pevm.Task) []pevm.StorageKey {
	tuples := o.bal[task.ID()]
	if len(tuples) == 0 {
		return nil
	}
	keys := make([]pevm.StorageKey, 0, len(tuples))
	for _, t := range tuples {
		if len(t.StorageKeys) == 0 {
			// Account-level key
			keys = append(keys, pevm.StorageKey{Address: t.Address, Slot: gethcommon.Hash{}, Type: o.keyType})
			continue
		}
		for _, slot := range t.StorageKeys {
			keys = append(keys, pevm.StorageKey{Address: t.Address, Slot: slot, Type: o.keyType})
		}
	}
	return keys
}

// FromEIP2930 converts a map of task IDs to access tuples into a StaticOracle.
// Use pevm.KeyTypeStorage for storage slots, or KeyTypeAccount for account-level hints.
func FromEIP2930(bal List, keyType pevm.KeyType) *StaticOracle {
	return NewStaticOracle(bal, keyType)
}

// AccessTupleInput is a lightweight representation of an EIP-2930 access tuple.
type AccessTupleInput struct {
	Address     gethcommon.Address
	StorageKeys []gethcommon.Hash
}

// AccessListInput is a lightweight representation of an EIP-2930 access list.
type AccessListInput []AccessTupleInput

// BuildListFromEIP2930 constructs a BAL List for the given tasks using their access lists.
// tasks and accessLists must be the same length and correspond by index.
func BuildListFromEIP2930(tasks []pevm.Task, accessLists []AccessListInput) List {
	out := make(List, len(tasks))
	n := len(tasks)
	if len(accessLists) < n {
		n = len(accessLists)
	}
	for i := 0; i < n; i++ {
		t := tasks[i]
		al := accessLists[i]
		tuples := make([]Tuple, 0, len(al))
		for _, at := range al {
			tuples = append(tuples, Tuple{Address: at.Address, StorageKeys: at.StorageKeys})
		}
		out[t.ID()] = tuples
	}
	return out
}
