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

package executor

import (
	"math/big"
	"time"

	"github.com/UranusBlockStack/uranus/common/crypto"
	"github.com/UranusBlockStack/uranus/common/utils"
	"github.com/UranusBlockStack/uranus/consensus"
	"github.com/UranusBlockStack/uranus/core/ledger"
	"github.com/UranusBlockStack/uranus/core/state"
	"github.com/UranusBlockStack/uranus/core/txpool"
	"github.com/UranusBlockStack/uranus/core/types"
	"github.com/UranusBlockStack/uranus/core/vm"
	"github.com/UranusBlockStack/uranus/params"
)

type ITxPool interface {
	AddAction(a *types.Action)
}

// Executor is a transactions executor
type Executor struct {
	config *params.ChainConfig // Chain configuration options
	ledger *ledger.Ledger      // ledger
	tp     ITxPool
	chain  consensus.IChainReader
	engine consensus.Engine
}

// NewExecutor initialises a new Executor.
func NewExecutor(config *params.ChainConfig, l *ledger.Ledger, chain consensus.IChainReader, engine consensus.Engine) *Executor {
	return &Executor{
		config: config,
		ledger: l,
		chain:  chain,
		engine: engine,
	}
}

func (e *Executor) SetTxPool(tp ITxPool) {
	e.tp = tp
}

// ExecBlock execute block
func (e *Executor) ExecBlock(block *types.Block, statedb *state.StateDB, cfg vm.Config) (types.Receipts, []*types.Log, uint64, error) {
	var (
		receipts types.Receipts
		usedGas  = new(uint64)
		header   = block.BlockHeader()
		allLogs  []*types.Log
		gp       = new(utils.GasPool).AddGas(block.GasLimit())
	)

	e.ExecActions(statedb, block.Actions())

	// Iterate over and process the individual transactions
	for i, tx := range block.Transactions() {
		statedb.Prepare(tx.Hash(), block.Hash(), i)
		receipt, _, err := e.ExecTransaction(nil, block.DposCtx(), gp, statedb, header, tx, usedGas, cfg)
		if err != nil {
			return nil, nil, 0, err
		}
		receipts = append(receipts, receipt)
		allLogs = append(allLogs, receipt.Logs...)
	}

	e.engine.Finalize(e.chain, header, statedb, block.Transactions(), block.Actions(), receipts, block.DposCtx())
	return receipts, allLogs, *usedGas, nil
}

// ExecActions execute actions
func (e *Executor) ExecActions(statedb *state.StateDB, actions []*types.Action) {
	for _, a := range actions {
		lb := statedb.GetLockedBalance(a.Sender)
		statedb.AddBalance(a.Sender, lb)
		statedb.SetLockedBalance(a.Sender, utils.Big0)
	}
}

// ExecTransaction attempts to execute a transaction to the given state database and uses the input parameters for its environment.
func (e *Executor) ExecTransaction(author *utils.Address,
	dposContext *types.DposContext,
	gp *utils.GasPool, statedb *state.StateDB, header *types.BlockHeader,
	tx *types.Transaction, usedGas *uint64, cfg vm.Config) (*types.Receipt, uint64, error) {
	var (
		gas    uint64
		failed bool
		err    error
	)

	if tx.Type() == types.Binary {

		// Create a new context to be used in the EVM environment
		context := NewEVMContext(tx, header, e.ledger, e.engine, author)
		// Create a new environment which holds all relevant informationabout the transaction and calling mechanisms.
		vmenv := vm.NewEVM(context, statedb, e.config, cfg)
		// Apply the transaction to the current state (included in the env)
		_, gas, failed, err = ExecStateTransition(vmenv, tx, gp)
		if err != nil {
			return nil, 0, err
		}
	} else {
		failed = e.applyDposMessage(dposContext, tx, statedb)
		gas, _ = txpool.IntrinsicGas(tx.Payload(), false)
	}

	root := statedb.IntermediateRoot(true).Bytes()
	*usedGas += gas

	receipt := types.NewReceipt(root, failed, *usedGas)
	receipt.TransactionHash = tx.Hash()
	receipt.GasUsed = gas
	// create contract
	if tx.Tos() == nil {
		from, _ := tx.Sender(types.Signer{})
		receipt.ContractAddress = crypto.CreateAddress(from, tx.Nonce())
	}
	// Set the receipt logs and create a bloom for filtering
	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.LogsBloom = types.CreateBloom(types.Receipts{receipt})
	return receipt, gas, err
}

func (e *Executor) applyDposMessage(dposContext *types.DposContext, tx *types.Transaction, statedb *state.StateDB) bool {
	from, _ := tx.Sender(types.Signer{})
	switch tx.Type() {
	case types.LoginCandidate:
		dposContext.BecomeCandidate(from)
	case types.LogoutCandidate:
		dposContext.KickoutCandidate(from)
	case types.Delegate:
		statedb.SetDelegateTimestamp(from, big.NewInt(time.Now().Unix()))
		statedb.SubBalance(from, tx.Value())
		statedb.SetLockedBalance(from, tx.Value())
		dposContext.Delegate(from, tx.Tos())
	case types.UnDelegate:
		statedb.ResetDelegateTimestamp(from)
		// todo validate tos

		e.addAction(from, tx)
		dposContext.UnDelegate(from, tx.Tos())
	case types.Redeem:
		timestamp := statedb.GetDelegateTimestamp(from)
		if new(big.Int).Sub(big.NewInt(time.Now().Unix()), timestamp).Cmp(e.chain.Config().DelayDuration) < 0 {
			return false
		}
		lockedBalance := statedb.GetLockedBalance(from)
		statedb.AddBalance(from, lockedBalance)
		statedb.UnLockBalance(from)
	}
	return true
}

func (e *Executor) addAction(sender utils.Address, tx *types.Transaction) {
	a := types.NewAction(tx.Hash(), sender, big.NewInt(time.Now().Unix()), e.chain.Config().DelayDuration)
	e.tp.AddAction(a)
}
