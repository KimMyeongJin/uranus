// Copyright 2018 The uranus Authors
// This file is part of the uranus library.
//
// The uranus library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The uranus library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the uranus library. If not, see <http://www.gnu.org/licenses/>.

package mtp

import (
	"sync"
	"time"

	ldb "github.com/UranusBlockStack/uranus/common/db"
	"github.com/UranusBlockStack/uranus/common/log"
	"github.com/UranusBlockStack/uranus/common/utils"
)

// secureKeyPrefix is the database key prefix used to store trie node preimages.
var secureKeyPrefix = []byte("secure-key-")

// secureKeyLength is the length of the above prefix + 32byte hash.
const secureKeyLength = 11 + 32

// DatabaseReader wraps the Get and Has method of a backing store for the trie.
type DatabaseReader interface {
	// Get retrieves the value associated with key form the database.
	Get(key []byte) (value []byte, err error)

	// Has retrieves whether a key is present in the database.
	Has(key []byte) (bool, error)
}

// DatabaseWriter wraps the Put method of a backing store for the trie.
type DatabaseWriter interface {
	// Put stores the mapping key->value in the database.
	// Implementations must not hold onto the value bytes, the trie
	// will reuse the slice across calls to Put.
	Put(key, value []byte) error
}

// Database is an intermediate write layer between the trie data structures and
// the disk database. The aim is to accumulate trie writes in-memory and only
// periodically flush a couple tries to disk, garbage collecting the remainder.
type Database struct {
	diskdb ldb.Database // Persistent storage for matured trie nodes

	nodes     map[utils.Hash]*cachedNode // Data and references relationships of a node
	preimages map[utils.Hash][]byte      // Preimages of nodes from the secure trie
	seckeybuf [secureKeyLength]byte      // Ephemeral buffer for calculating preimage keys

	gctime  time.Duration     // Time spent on garbage collection since last commit
	gcnodes uint64            // Nodes garbage collected since last commit
	gcsize  utils.StorageSize // Data storage garbage collected since last commit

	nodesSize     utils.StorageSize // Storage size of the nodes cache
	preimagesSize utils.StorageSize // Storage size of the preimages cache

	lock sync.RWMutex
}

// cachedNode is all the information we know about a single cached node in the
// memory database write layer.
type cachedNode struct {
	blob     []byte             // Cached data block of the trie node
	parents  int                // Number of live nodes referencing this one
	children map[utils.Hash]int // Children referenced by this nodes
}

// NewDatabase creates a new trie database to store ephemeral trie content before
// its written out to disk or garbage collected.
func NewDatabase(diskdb ldb.Database) *Database {
	return &Database{
		diskdb: diskdb,
		nodes: map[utils.Hash]*cachedNode{
			{}: {children: make(map[utils.Hash]int)},
		},
		preimages: make(map[utils.Hash][]byte),
	}
}

// DiskDB retrieves the persistent storage backing the trie database.
func (db *Database) DiskDB() DatabaseReader {
	return db.diskdb
}

// Insert writes a new trie node to the memory database if it's yet unknown. The
// method will make a copy of the slice.
func (db *Database) Insert(hash utils.Hash, blob []byte) {
	db.lock.Lock()
	defer db.lock.Unlock()

	db.insert(hash, blob)
}

// insert is the private locked version of Insert.
func (db *Database) insert(hash utils.Hash, blob []byte) {
	if _, ok := db.nodes[hash]; ok {
		return
	}
	db.nodes[hash] = &cachedNode{
		blob:     utils.CopyBytes(blob),
		children: make(map[utils.Hash]int),
	}
	db.nodesSize += utils.StorageSize(utils.HashLength + len(blob))
}

// insertPreimage writes a new trie node pre-image to the memory database if it's
// yet unknown. The method will make a copy of the slice.
//
// Note, this method assumes that the database's lock is held!
func (db *Database) insertPreimage(hash utils.Hash, preimage []byte) {
	if _, ok := db.preimages[hash]; ok {
		return
	}
	db.preimages[hash] = utils.CopyBytes(preimage)
	db.preimagesSize += utils.StorageSize(utils.HashLength + len(preimage))
}

// Node retrieves a cached trie node from memory. If it cannot be found cached,
// the method queries the persistent database for the content.
func (db *Database) Node(hash utils.Hash) ([]byte, error) {
	// Retrieve the node from cache if available
	db.lock.RLock()
	node := db.nodes[hash]
	db.lock.RUnlock()

	if node != nil {
		return node.blob, nil
	}
	// Content unavailable in memory, attempt to retrieve from disk
	return db.diskdb.Get(hash[:])
}

// preimage retrieves a cached trie node pre-image from memory. If it cannot be
// found cached, the method queries the persistent database for the content.
func (db *Database) preimage(hash utils.Hash) ([]byte, error) {
	// Retrieve the node from cache if available
	db.lock.RLock()
	preimage := db.preimages[hash]
	db.lock.RUnlock()

	if preimage != nil {
		return preimage, nil
	}
	// Content unavailable in memory, attempt to retrieve from disk
	return db.diskdb.Get(db.secureKey(hash[:]))
}

// secureKey returns the database key for the preimage of key, as an ephemeral
// buffer. The caller must not hold onto the return value because it will become
// invalid on the next call.
func (db *Database) secureKey(key []byte) []byte {
	buf := append(db.seckeybuf[:0], secureKeyPrefix...)
	buf = append(buf, key...)
	return buf
}

// Nodes retrieves the hashes of all the nodes cached within the memory database.
// This method is extremely expensive and should only be used to validate internal
// states in test code.
func (db *Database) Nodes() []utils.Hash {
	db.lock.RLock()
	defer db.lock.RUnlock()

	var hashes = make([]utils.Hash, 0, len(db.nodes))
	for hash := range db.nodes {
		if hash != (utils.Hash{}) { // Special case for "root" references/nodes
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

// Reference adds a new reference from a parent node to a child node.
func (db *Database) Reference(child utils.Hash, parent utils.Hash) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	db.reference(child, parent)
}

// reference is the private locked version of Reference.
func (db *Database) reference(child utils.Hash, parent utils.Hash) {
	// If the node does not exist, it's a node pulled from disk, skip
	node, ok := db.nodes[child]
	if !ok {
		return
	}
	// If the reference already exists, only duplicate for roots
	if _, ok = db.nodes[parent].children[child]; ok && parent != (utils.Hash{}) {
		return
	}
	node.parents++
	db.nodes[parent].children[child]++
}

// Dereference removes an existing reference from a parent node to a child node.
func (db *Database) Dereference(child utils.Hash, parent utils.Hash) {
	db.lock.Lock()
	defer db.lock.Unlock()

	nodes, storage, start := len(db.nodes), db.nodesSize, time.Now()
	db.dereference(child, parent)

	db.gcnodes += uint64(nodes - len(db.nodes))
	db.gcsize += storage - db.nodesSize
	db.gctime += time.Since(start)

	log.Debugf("Dereferenced trie from memory database nodes: %v,size: %v,time: %v,gcnodes: %v,gcsize: %v,gctime: %v,livenodes: %v,livesize: %v",
		nodes-len(db.nodes), storage-db.nodesSize, time.Since(start), db.gcnodes, db.gcsize, db.gctime, len(db.nodes), db.nodesSize)
}

// dereference is the private locked version of Dereference.
func (db *Database) dereference(child utils.Hash, parent utils.Hash) {
	// Dereference the parent-child
	node := db.nodes[parent]

	node.children[child]--
	if node.children[child] == 0 {
		delete(node.children, child)
	}
	// If the node does not exist, it's a previously committed node.
	node, ok := db.nodes[child]
	if !ok {
		return
	}
	// If there are no more references to the child, delete it and cascade
	node.parents--
	if node.parents == 0 {
		for hash := range node.children {
			db.dereference(hash, child)
		}
		delete(db.nodes, child)
		db.nodesSize -= utils.StorageSize(utils.HashLength + len(node.blob))
	}
}

// Commit iterates over all the children of a particular node, writes them out
// to disk, forcefully tearing down all references in both directions.
//
// As a side effect, all pre-images accumulated up to this point are also written.
func (db *Database) Commit(node utils.Hash, report bool) error {
	// Create a database batch to flush persistent data out. It is important that
	// outside code doesn't see an inconsistent state (referenced data removed from
	// memory cache during commit but not yet in persistent storage). This is ensured
	// by only uncaching existing data when the database write finalizes.
	db.lock.RLock()

	start := time.Now()
	batch := db.diskdb.NewBatch()

	// Move all of the accumulated preimages into a write batch
	for hash, preimage := range db.preimages {
		if err := batch.Put(db.secureKey(hash[:]), preimage); err != nil {
			log.Errorf("Failed to commit preimage from trie database err: %v", err)
			db.lock.RUnlock()
			return err
		}
		if batch.ValueSize() > ldb.IdealBatchSize {
			if err := batch.Write(); err != nil {
				return err
			}
			batch.Reset()
		}
	}
	// Move the trie itself into the batch, flushing if enough data is accumulated
	nodes, storage := len(db.nodes), db.nodesSize+db.preimagesSize
	if err := db.commit(node, batch); err != nil {
		log.Errorf("Failed to commit trie from trie database err: %v", err)
		db.lock.RUnlock()
		return err
	}
	// Write batch ready, unlock for readers during persistence
	if err := batch.Write(); err != nil {
		log.Errorf("Failed to write trie to disk err: %v", err)
		db.lock.RUnlock()
		return err
	}
	db.lock.RUnlock()

	// Write successful, clear out the flushed data
	db.lock.Lock()
	defer db.lock.Unlock()

	db.preimages = make(map[utils.Hash][]byte)
	db.preimagesSize = 0

	db.uncache(node)

	logger := log.Infof
	if !report {
		logger = log.Debugf
	}
	logger("Persisted trie from memory database nodes: %v,size :%v,time: %v,gcnodes: %v,gcsize: %v,gctime: %v,livenodes: %v,livesize: %v",
		nodes-len(db.nodes), storage-db.nodesSize, time.Since(start), db.gcnodes, db.gcsize, db.gctime, len(db.nodes), db.nodesSize)
	// Reset the garbage collection statistics
	db.gcnodes, db.gcsize, db.gctime = 0, 0, 0

	return nil
}

// commit is the private locked version of Commit.
func (db *Database) commit(hash utils.Hash, batch ldb.Batch) error {
	// If the node does not exist, it's a previously committed node
	node, ok := db.nodes[hash]
	if !ok {
		return nil
	}
	for child := range node.children {
		if err := db.commit(child, batch); err != nil {
			return err
		}
	}
	if err := batch.Put(hash[:], node.blob); err != nil {
		return err
	}
	// If we've reached an optimal match size, commit and start over
	if batch.ValueSize() >= ldb.IdealBatchSize {
		if err := batch.Write(); err != nil {
			return err
		}
		batch.Reset()
	}
	return nil
}

// uncache is the post-processing step of a commit operation where the already
// persisted trie is removed from the cache. The reason behind the two-phase
// commit is to ensure consistent data availability while moving from memory
// to disk.
func (db *Database) uncache(hash utils.Hash) {
	// If the node does not exist, we're done on this path
	node, ok := db.nodes[hash]
	if !ok {
		return
	}
	// Otherwise uncache the node's subtries and remove the node itself too
	for child := range node.children {
		db.uncache(child)
	}
	delete(db.nodes, hash)
	db.nodesSize -= utils.StorageSize(utils.HashLength + len(node.blob))
}

// Size returns the current storage size of the memory cache in front of the
// persistent database layer.
func (db *Database) Size() utils.StorageSize {
	db.lock.RLock()
	defer db.lock.RUnlock()

	return db.nodesSize + db.preimagesSize
}
