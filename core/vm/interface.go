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

package vm

import (
	"math/big"

	"github.com/UranusBlockStack/uranus/common/utils"
	"github.com/UranusBlockStack/uranus/core/types"
)

// StateDB is an EVM database for full state querying.
type StateDB interface {
	CreateAccount(utils.Address)

	SubBalance(utils.Address, *big.Int)
	AddBalance(utils.Address, *big.Int)
	GetBalance(utils.Address) *big.Int

	GetNonce(utils.Address) uint64
	SetNonce(utils.Address, uint64)

	GetCodeHash(utils.Address) utils.Hash
	GetCode(utils.Address) []byte
	SetCode(utils.Address, []byte)
	GetCodeSize(utils.Address) int

	AddRefund(uint64)
	GetRefund() uint64

	GetState(utils.Address, utils.Hash) utils.Hash
	SetState(utils.Address, utils.Hash, utils.Hash)

	Suicide(utils.Address) bool
	HasSuicided(utils.Address) bool

	// Exist reports whether the given account exists in state.
	// Notably this should also return true for suicided accounts.
	Exist(utils.Address) bool
	// Empty returns whether the given account is empty. Empty
	// is defined according to EIP161 (balance = nonce = code = 0).
	Empty(utils.Address) bool

	RevertToSnapshot(int)
	Snapshot() int

	AddLog(*types.Log)
	AddPreimage(utils.Hash, []byte)

	ForEachStorage(utils.Address, func(utils.Hash, utils.Hash) bool)
}

// CallContext provides a basic interface for the EVM calling conventions. The EVM EVM
// depends on this context being implemented for doing subcalls and initialising new EVM contracts.
type CallContext interface {
	// Call another contract
	Call(env *EVM, me ContractRef, addr utils.Address, data []byte, gas, value *big.Int) ([]byte, error)
	// Take another's contract code and execute within our own context
	CallCode(env *EVM, me ContractRef, addr utils.Address, data []byte, gas, value *big.Int) ([]byte, error)
	// Same as CallCode except sender and value is propagated from parent to child scope
	DelegateCall(env *EVM, me ContractRef, addr utils.Address, data []byte, gas *big.Int) ([]byte, error)
	// Create a new contract
	Create(env *EVM, me ContractRef, data []byte, gas, value *big.Int) ([]byte, utils.Address, error)
}
