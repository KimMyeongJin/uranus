// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package state

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	check "gopkg.in/check.v1"

	ldb "github.com/UranusBlockStack/uranus/common/db"
	"github.com/UranusBlockStack/uranus/common/utils"
	"github.com/UranusBlockStack/uranus/core/types"
)

// Tests that updating a state trie does not leak any database writes prior to
// actually committing the state.
func TestUpdateLeaks(t *testing.T) {
	// Create an empty state database
	db := ldb.NewMemDatabase()
	state, _ := New(utils.Hash{}, NewDatabase(db))

	// Update it with some accounts
	for i := byte(0); i < 255; i++ {
		addr := utils.BytesToAddress([]byte{i})
		state.AddBalance(addr, big.NewInt(int64(11*i)))
		state.SetNonce(addr, uint64(42*i))

		state.SetLockedBalance(addr, big.NewInt(int64(41*i)))
		addr0 := utils.BytesToAddress([]byte{0})
		addr1 := utils.BytesToAddress([]byte{1})
		state.SetDelegateAddresses(addr, []*utils.Address{&addr0, &addr1})
		state.SetDelegateTimestamp(addr, big.NewInt(time.Now().Unix()))

		if i%2 == 0 {
			state.SetState(addr, utils.BytesToHash([]byte{i, i, i}), utils.BytesToHash([]byte{i, i, i, i}))
		}
		if i%3 == 0 {
			state.SetCode(addr, []byte{i, i, i, i, i})
		}
		state.IntermediateRoot(false)
	}
	// Ensure that no data was leaked into the database
	for _, key := range db.Keys() {
		value, _ := db.Get(key)
		t.Errorf("State leaked into database: %x -> %x", key, value)
	}
}

// Tests that no intermediate state of an object is stored into the database,
// only the one right before the commit.
func TestIntermediateLeaks(t *testing.T) {
	// Create two state databases, one transitioning to the final state, the other final from the beginning
	transDb := ldb.NewMemDatabase()
	finalDb := ldb.NewMemDatabase()
	transState, _ := New(utils.Hash{}, NewDatabase(transDb))
	finalState, _ := New(utils.Hash{}, NewDatabase(finalDb))

	modify := func(state *StateDB, addr utils.Address, i, tweak byte) {
		state.SetBalance(addr, big.NewInt(int64(11*i)+int64(tweak)))
		state.SetNonce(addr, uint64(42*i+tweak))
		state.SetLockedBalance(addr, big.NewInt(int64(41*i)+int64(tweak)))
		addr0 := utils.BytesToAddress([]byte{0})
		addr1 := utils.BytesToAddress([]byte{1})
		state.SetDelegateAddresses(addr, []*utils.Address{&addr0, &addr1})
		state.SetDelegateTimestamp(addr, big.NewInt(time.Now().Unix()))

		if i%2 == 0 {
			state.SetState(addr, utils.Hash{i, i, i, 0}, utils.Hash{})
			state.SetState(addr, utils.Hash{i, i, i, tweak}, utils.Hash{i, i, i, i, tweak})
		}
		if i%3 == 0 {
			state.SetCode(addr, []byte{i, i, i, i, i, tweak})
		}
	}

	// Modify the transient state.
	for i := byte(0); i < 255; i++ {
		modify(transState, utils.Address{byte(i)}, i, 0)
	}
	// Write modifications to trie.
	transState.IntermediateRoot(false)

	// Overwrite all the data with new values in the transient database.
	for i := byte(0); i < 255; i++ {
		modify(transState, utils.Address{byte(i)}, i, 99)
		modify(finalState, utils.Address{byte(i)}, i, 99)
	}

	// Commit and cross check the databases.
	if _, err := transState.Commit(false); err != nil {
		t.Fatalf("failed to commit transition state: %v", err)
	}
	if _, err := finalState.Commit(false); err != nil {
		t.Fatalf("failed to commit final state: %v", err)
	}
	for _, key := range finalDb.Keys() {
		if _, err := transDb.Get(key); err != nil {
			val, _ := finalDb.Get(key)
			t.Errorf("entry missing from the transition database: %x -> %x", key, val)
		}
	}
	for _, key := range transDb.Keys() {
		if _, err := finalDb.Get(key); err != nil {
			val, _ := transDb.Get(key)
			t.Errorf("extra entry in the transition database: %x -> %x", key, val)
		}
	}
}

// TestCopy tests that copying a statedb object indeed makes the original and
// the copy independent of each other. This test is a regression test against
// https://github.com/ethereum/go-ethereum/pull/15549.
func TestCopy(t *testing.T) {
	// Create a random state test to copy and modify "independently"
	orig, _ := New(utils.Hash{}, NewDatabase(ldb.NewMemDatabase()))

	for i := byte(0); i < 255; i++ {
		obj := orig.GetOrNewStateObject(utils.BytesToAddress([]byte{i}))
		obj.AddBalance(big.NewInt(int64(i)))
		obj.LockBalance(big.NewInt(int64(i)))

		orig.updateStateObject(obj)
	}
	orig.Finalise(false)

	// Copy the state, modify both in-memory
	copy := orig.Copy()

	for i := byte(0); i < 255; i++ {
		origObj := orig.GetOrNewStateObject(utils.BytesToAddress([]byte{i}))
		copyObj := copy.GetOrNewStateObject(utils.BytesToAddress([]byte{i}))

		origObj.AddBalance(big.NewInt(2 * int64(i)))
		origObj.LockBalance(big.NewInt(2 * int64(i)))

		copyObj.AddBalance(big.NewInt(3 * int64(i)))
		copyObj.LockBalance(big.NewInt(3 * int64(i)))

		orig.updateStateObject(origObj)
		copy.updateStateObject(copyObj)
	}
	// Finalise the changes on both concurrently
	done := make(chan struct{})
	go func() {
		orig.Finalise(true)
		close(done)
	}()
	copy.Finalise(true)
	<-done

	// Verify that the two states have been updated independently
	for i := byte(0); i < 255; i++ {
		origObj := orig.GetOrNewStateObject(utils.BytesToAddress([]byte{i}))
		copyObj := copy.GetOrNewStateObject(utils.BytesToAddress([]byte{i}))

		if want := big.NewInt(3 * int64(i)); origObj.Balance().Cmp(want) != 0 {
			t.Errorf("orig obj %d: balance mismatch: have %v, want %v", i, origObj.Balance(), want)
		}
		if want := big.NewInt(4 * int64(i)); copyObj.Balance().Cmp(want) != 0 {
			t.Errorf("copy obj %d: balance mismatch: have %v, want %v", i, copyObj.Balance(), want)
		}
		if want := big.NewInt(2 * int64(i)); origObj.LockedBalance().Cmp(want) != 0 {
			t.Errorf("orig obj %d: locked balance mismatch: have %v, want %v", i, origObj.LockedBalance(), want)
		}
		if want := big.NewInt(3 * int64(i)); copyObj.LockedBalance().Cmp(want) != 0 {
			t.Errorf("copy obj %d: locked balance mismatch: have %v, want %v", i, copyObj.LockedBalance(), want)
		}
	}
}

func TestSnapshotRandom(t *testing.T) {
	config := &quick.Config{MaxCount: 1000}
	err := quick.Check((*snapshotTest).run, config)
	if cerr, ok := err.(*quick.CheckError); ok {
		test := cerr.In[0].(*snapshotTest)
		t.Errorf("%v:\n%s", test.err, test)
	} else if err != nil {
		t.Error(err)
	}
}

// A snapshotTest checks that reverting StateDB snapshots properly undoes all changes
// captured by the snapshot. Instances of this test with pseudorandom content are created
// by Generate.
//
// The test works as follows:
//
// A new state is created and all actions are applied to it. Several snapshots are taken
// in between actions. The test then reverts each snapshot. For each snapshot the actions
// leading up to it are replayed on a fresh, empty state. The behaviour of all public
// accessor methods on the reverted state must match the return value of the equivalent
// methods on the replayed state.
type snapshotTest struct {
	addrs     []utils.Address // all account addresses
	actions   []testAction    // modifications to the state
	snapshots []int           // actions indexes at which snapshot is taken
	err       error           // failure details are reported through this field
}

type testAction struct {
	name   string
	fn     func(testAction, *StateDB)
	args   []int64
	noAddr bool
}

// newTestAction creates a random action that changes state.
func newTestAction(addr utils.Address, r *rand.Rand) testAction {
	actions := []testAction{
		{
			name: "SetBalance",
			fn: func(a testAction, s *StateDB) {
				s.SetBalance(addr, big.NewInt(a.args[0]))
			},
			args: make([]int64, 1),
		},
		{
			name: "AddBalance",
			fn: func(a testAction, s *StateDB) {
				s.AddBalance(addr, big.NewInt(a.args[0]))
			},
			args: make([]int64, 1),
		},
		{
			name: "SetNonce",
			fn: func(a testAction, s *StateDB) {
				s.SetNonce(addr, uint64(a.args[0]))
			},
			args: make([]int64, 1),
		},
		{
			name: "SetLockedBalance",
			fn: func(a testAction, s *StateDB) {
				s.SetLockedBalance(addr, big.NewInt(a.args[0]))
			},
			args: make([]int64, 1),
		}, {
			name: "SetDelegateAddresses",
			fn: func(a testAction, s *StateDB) {
				addcode := make([]byte, 20)
				binary.BigEndian.PutUint64(addcode, uint64(a.args[0]))
				testAddr := utils.BytesToAddress(addcode)
				s.SetDelegateAddresses(addr, []*utils.Address{&testAddr})
			},
			args: make([]int64, 1),
		}, {
			name: "SetDelegateTimestamp",
			fn: func(a testAction, s *StateDB) {
				s.SetDelegateTimestamp(addr, big.NewInt(a.args[0]))
			},
			args: make([]int64, 1),
		},
		{
			name: "SetState",
			fn: func(a testAction, s *StateDB) {
				var key, val utils.Hash
				binary.BigEndian.PutUint16(key[:], uint16(a.args[0]))
				binary.BigEndian.PutUint16(val[:], uint16(a.args[1]))
				s.SetState(addr, key, val)
			},
			args: make([]int64, 2),
		},
		{
			name: "SetCode",
			fn: func(a testAction, s *StateDB) {
				code := make([]byte, 16)
				binary.BigEndian.PutUint64(code, uint64(a.args[0]))
				binary.BigEndian.PutUint64(code[8:], uint64(a.args[1]))
				s.SetCode(addr, code)
			},
			args: make([]int64, 2),
		},
		{
			name: "CreateAccount",
			fn: func(a testAction, s *StateDB) {
				s.CreateAccount(addr)
			},
		},
		{
			name: "Suicide",
			fn: func(a testAction, s *StateDB) {
				s.Suicide(addr)
			},
		},
		{
			name: "AddRefund",
			fn: func(a testAction, s *StateDB) {
				s.AddRefund(uint64(a.args[0]))
			},
			args:   make([]int64, 1),
			noAddr: true,
		},
		{
			name: "AddLog",
			fn: func(a testAction, s *StateDB) {
				data := make([]byte, 2)
				binary.BigEndian.PutUint16(data, uint16(a.args[0]))
				s.AddLog(&types.Log{Address: addr, Data: data})
			},
			args: make([]int64, 1),
		},
	}
	action := actions[r.Intn(len(actions))]
	var nameargs []string
	if !action.noAddr {
		nameargs = append(nameargs, addr.Hex())
	}
	for _, i := range action.args {
		action.args[i] = rand.Int63n(100)
		nameargs = append(nameargs, fmt.Sprint(action.args[i]))
	}
	action.name += strings.Join(nameargs, ", ")
	return action
}

// Generate returns a new snapshot test of the given size. All randomness is
// derived from r.
func (*snapshotTest) Generate(r *rand.Rand, size int) reflect.Value {
	// Generate random actions.
	addrs := make([]utils.Address, 50)
	for i := range addrs {
		addrs[i][0] = byte(i)
	}
	actions := make([]testAction, size)
	for i := range actions {
		addr := addrs[r.Intn(len(addrs))]
		actions[i] = newTestAction(addr, r)
	}
	// Generate snapshot indexes.
	nsnapshots := int(math.Sqrt(float64(size)))
	if size > 0 && nsnapshots == 0 {
		nsnapshots = 1
	}
	snapshots := make([]int, nsnapshots)
	snaplen := len(actions) / nsnapshots
	for i := range snapshots {
		// Try to place the snapshots some number of actions apart from each other.
		snapshots[i] = (i * snaplen) + r.Intn(snaplen)
	}
	return reflect.ValueOf(&snapshotTest{addrs, actions, snapshots, nil})
}

func (test *snapshotTest) String() string {
	out := new(bytes.Buffer)
	sindex := 0
	for i, action := range test.actions {
		if len(test.snapshots) > sindex && i == test.snapshots[sindex] {
			fmt.Fprintf(out, "---- snapshot %d ----\n", sindex)
			sindex++
		}
		fmt.Fprintf(out, "%4d: %s\n", i, action.name)
	}
	return out.String()
}

func (test *snapshotTest) run() bool {
	// Run all actions and create snapshots.
	var (
		state, _     = New(utils.Hash{}, NewDatabase(ldb.NewMemDatabase()))
		snapshotRevs = make([]int, len(test.snapshots))
		sindex       = 0
	)
	for i, action := range test.actions {
		if len(test.snapshots) > sindex && i == test.snapshots[sindex] {
			snapshotRevs[sindex] = state.Snapshot()
			sindex++
		}
		action.fn(action, state)
	}
	// Revert all snapshots in reverse order. Each revert must yield a state
	// that is equivalent to fresh state with all actions up the snapshot applied.
	for sindex--; sindex >= 0; sindex-- {
		checkstate, _ := New(utils.Hash{}, state.Database())
		for _, action := range test.actions[:test.snapshots[sindex]] {
			action.fn(action, checkstate)
		}
		state.RevertToSnapshot(snapshotRevs[sindex])
		if err := test.checkEqual(state, checkstate); err != nil {
			test.err = fmt.Errorf("state mismatch after revert to snapshot %d\n%v", sindex, err)
			return false
		}
	}
	return true
}

// checkEqual checks that methods of state and checkstate return the same values.
func (test *snapshotTest) checkEqual(state, checkstate *StateDB) error {
	for _, addr := range test.addrs {
		var err error
		checkeq := func(op string, a, b interface{}) bool {
			if err == nil && !reflect.DeepEqual(a, b) {
				err = fmt.Errorf("got %s(%s) == %v, want %v", op, addr.Hex(), a, b)
				return false
			}
			return true
		}
		// Check basic accessor methods.
		checkeq("Exist", state.Exist(addr), checkstate.Exist(addr))
		checkeq("HasSuicided", state.HasSuicided(addr), checkstate.HasSuicided(addr))
		checkeq("GetBalance", state.GetBalance(addr), checkstate.GetBalance(addr))
		checkeq("GetNonce", state.GetNonce(addr), checkstate.GetNonce(addr))
		checkeq("GetLockedBalance", state.GetLockedBalance(addr), checkstate.GetLockedBalance(addr))
		checkeq("GetDelegateTimestamp", state.GetDelegateTimestamp(addr), checkstate.GetDelegateTimestamp(addr))
		checkeq("GetDelegateAddresses", state.GetDelegateAddresses(addr), checkstate.GetDelegateAddresses(addr))
		checkeq("GetCode", state.GetCode(addr), checkstate.GetCode(addr))
		checkeq("GetCodeHash", state.GetCodeHash(addr), checkstate.GetCodeHash(addr))
		checkeq("GetCodeSize", state.GetCodeSize(addr), checkstate.GetCodeSize(addr))
		// Check storage.
		if obj := state.getStateObject(addr); obj != nil {
			state.ForEachStorage(addr, func(key, val utils.Hash) bool {
				return checkeq("GetState("+key.Hex()+")", val, checkstate.GetState(addr, key))
			})
			checkstate.ForEachStorage(addr, func(key, checkval utils.Hash) bool {
				return checkeq("GetState("+key.Hex()+")", state.GetState(addr, key), checkval)
			})
		}
		if err != nil {
			return err
		}
	}

	if state.GetRefund() != checkstate.GetRefund() {
		return fmt.Errorf("got GetRefund() == %d, want GetRefund() == %d",
			state.GetRefund(), checkstate.GetRefund())
	}
	if !reflect.DeepEqual(state.GetLogs(utils.Hash{}), checkstate.GetLogs(utils.Hash{})) {
		return fmt.Errorf("got GetLogs(utils.Hash{}) == %v, want GetLogs(utils.Hash{}) == %v",
			state.GetLogs(utils.Hash{}), checkstate.GetLogs(utils.Hash{}))
	}
	return nil
}

func (s *StateSuite) TestTouchDelete(c *check.C) {
	s.state.GetOrNewStateObject(utils.Address{})
	root, _ := s.state.Commit(false)
	s.state.Reset(root)

	snapshot := s.state.Snapshot()
	s.state.AddBalance(utils.Address{}, new(big.Int))

	if len(s.state.journal.dirties) != 1 {
		c.Fatal("expected one dirty state object")
	}
	s.state.RevertToSnapshot(snapshot)
	if len(s.state.journal.dirties) != 0 {
		c.Fatal("expected no dirty state object")
	}
}

// TestCopyOfCopy tests that modified objects are carried over to the copy, and the copy of the copy.
// See https://github.com/ethereum/go-ethereum/pull/15225#issuecomment-380191512
func TestCopyOfCopy(t *testing.T) {
	sdb, _ := New(utils.Hash{}, NewDatabase(ldb.NewMemDatabase()))
	addr := utils.HexToAddress("aaaa")
	sdb.SetBalance(addr, big.NewInt(42))
	sdb.SetLockedBalance(addr, big.NewInt(41))

	if got := sdb.Copy().GetBalance(addr).Uint64(); got != 42 {
		t.Fatalf("1st copy fail, expected 42, got %v", got)
	}
	if got := sdb.Copy().Copy().GetBalance(addr).Uint64(); got != 42 {
		t.Fatalf("2nd copy fail, expected 42, got %v", got)
	}

	if got := sdb.Copy().GetLockedBalance(addr).Uint64(); got != 41 {
		t.Fatalf("1st copy fail, expected 42, got %v", got)
	}
	if got := sdb.Copy().Copy().GetLockedBalance(addr).Uint64(); got != 41 {
		t.Fatalf("2nd copy fail, expected 42, got %v", got)
	}
}
