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

package miner

import (
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/UranusBlockStack/uranus/common/db"
	"github.com/UranusBlockStack/uranus/common/log"
	"github.com/UranusBlockStack/uranus/common/mtp"
	"github.com/UranusBlockStack/uranus/common/utils"
	"github.com/UranusBlockStack/uranus/consensus"
	"github.com/UranusBlockStack/uranus/consensus/dpos"
	"github.com/UranusBlockStack/uranus/core/types"
	"github.com/UranusBlockStack/uranus/feed"
	"github.com/UranusBlockStack/uranus/node/protocols"
	"github.com/UranusBlockStack/uranus/params"
)

type Config struct {
	CoinBaseAddr string `mapstructure:"miner-coinbase"`
	MinerThreads int    `mapstructure:"miner-threads"`
	ExtraData    string `mapstructure:"miner-extradata"`
}

type UMiner struct {
	mu            sync.Mutex
	wg            sync.WaitGroup
	mining        int32
	canStart      int32
	stopCh        chan struct{}
	quitCurrentOp chan struct{}
	uranus        consensus.IUranus
	db            db.Database

	extraData   []byte
	coinbase    utils.Address
	currentWork *Work
	engine      consensus.Engine
	config      *params.ChainConfig

	mux *feed.TypeMux
}

func NewUranusMiner(mux *feed.TypeMux, config *params.ChainConfig, minerCfg *Config, uranus consensus.IUranus, engine consensus.Engine, db db.Database) *UMiner {
	coinbase := utils.HexToAddress(minerCfg.CoinBaseAddr)
	uminer := &UMiner{
		mux:       mux,
		config:    config,
		uranus:    uranus,
		mining:    0,
		canStart:  1,
		stopCh:    make(chan struct{}),
		extraData: []byte(minerCfg.ExtraData),
		coinbase:  coinbase,
		engine:    engine,
		db:        db,
	}
	go uminer.loop()
	return uminer
}

func (m *UMiner) loop() {
	events := m.mux.Subscribe(protocols.StartEvent{}, protocols.DoneEvent{}, protocols.FailedEvent{})
	minning := int32(0)
out:
	for ev := range events.Chan() {
		switch ev.Data.(type) {
		case protocols.StartEvent:
			atomic.StoreInt32(&m.canStart, 0)
			minning = atomic.LoadInt32(&m.mining)
			if minning == 1 {
				log.Warnf("Mining operation maybe aborted due to sync operation")
				m.Stop()
			}
		case protocols.DoneEvent, protocols.FailedEvent:
			atomic.StoreInt32(&m.canStart, 1)
			if minning == 1 {
				log.Warnf("Mining operation maybe start due to sync done or sync failed")
				if err := m.Start(); err != nil {
					log.Errorf("Mining operation start failed --- %v", err)
				}
			}
			events.Unsubscribe()
			break out
		}
	}
}

func (m *UMiner) Start() error {
	if atomic.LoadInt32(&m.canStart) == 0 {
		log.Info("Can not start miner when syncing")
		return fmt.Errorf("node is syncing now")
	}
	if atomic.LoadInt32(&m.mining) == 1 {
		log.Info("Miner is running")
		return fmt.Errorf("miner is running")
	}
	m.stopCh = make(chan struct{})

	// CAS to ensure only 1 mining goroutine.
	if !atomic.CompareAndSwapInt32(&m.mining, 0, 1) {
		log.Warn("Another goroutine has already started to mine")
		return nil
	}
	m.uranus.PostEvent(feed.NewMiner{})

	m.wg.Add(2)
	go m.update()
	go m.mintLoop()

	// if err := m.prepareNewBlock(); err != nil { // try to prepare the first block
	// 	log.Warnf("mining prepareNewBlock err: %v", err)
	// 	atomic.StoreInt32(&m.mining, 0)
	// 	return err
	// }

	m.SetCoinBase(m.coinbase)
	log.Info("Miner is started.")
	return nil
}

func (m *UMiner) Stop() {
	if !atomic.CompareAndSwapInt32(&m.mining, 1, 0) {
		return
	}
	// notify all threads to terminate
	if m.stopCh != nil {
		close(m.stopCh)
	}

	m.wg.Wait()
	log.Info("Miner is stopped.")
}

func (m *UMiner) update() {
	defer m.wg.Done()
	// Subscribe TxPreEvent for tx pool
	// Subscribe events for blockchain
	chainBlockCh := make(chan feed.BlockAndLogsEvent, 10)
	chainBlockSub := m.uranus.SubscribeChainBlockEvent(chainBlockCh)
	defer chainBlockSub.Unsubscribe()
	txCh := make(chan feed.NewTxsEvent, 4096)
	txSub := m.uranus.SubscribeNewTxsEvent(txCh)
	defer txSub.Unsubscribe()
out:
	for {
		select {
		case <-chainBlockCh:

		case ev := <-txCh:
			if atomic.LoadInt32(&m.mining) == 0 {
				_ = ev.Txs
				txs := make(map[utils.Address]types.Transactions)
				for _, tx := range ev.Txs {
					acc, _ := tx.Sender(m.currentWork.signer)
					txs[acc] = append(txs[acc], tx)
				}
				txset := types.NewTransactionsByPriceAndNonce(m.currentWork.signer, txs)
				m.currentWork.applyTransactions(m.uranus, txset, time.Now().Add(time.Minute).UnixNano())
			}
		case <-chainBlockSub.Err():
			break out
		case <-txSub.Err():
			break out
		case <-m.stopCh:
			break out
		}
	}
	log.Debug("miner update to generate block thread quit ...")
}

func (m *UMiner) SetCoinBase(addr utils.Address) {
	m.mu.Lock()
	m.coinbase = addr
	m.mu.Unlock()
	if dpos, ok := m.engine.(*dpos.Dpos); ok {
		dpos.SetCoinBase(m.coinbase)
	}
	//m.prepareNewBlock()
}

func (m *UMiner) GetCoinBase() utils.Address {
	return m.coinbase
}

func (m *UMiner) PendingBlock() *types.Block {
	if m.currentWork == nil {
		return nil
	}
	return m.currentWork.Block
}

func calcGasLimit(parent *types.Block) uint64 {
	// contrib = (parentGasUsed * 3 / 2) / 1024
	contrib := (parent.GasUsed() + parent.GasUsed()/2) / params.GasLimitBoundDivisor
	// decay = parentGasLimit / 1024 -1
	decay := parent.GasLimit()/params.GasLimitBoundDivisor - 1
	limit := parent.GasLimit() - decay + contrib
	if limit < params.MinGasLimit {
		limit = params.MinGasLimit
	}
	// however, if we're now below the target (TargetGasLimit) we increase the
	// limit as much as we can (parentGasLimit / 1024 -1)
	if limit < params.GenesisGasLimit {
		limit = parent.GasLimit() + decay
		if limit > params.GenesisGasLimit {
			limit = params.GenesisGasLimit
		}
	}
	return limit
}

func (m *UMiner) mintLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Second)
	if _, ok := m.engine.(*dpos.Dpos); !ok {
		ticker.Stop()
	} else {
		ticker.Stop()
		time.Sleep(time.Duration(dpos.Option.BlockInterval - int64(time.Now().UnixNano())%dpos.Option.BlockInterval))
		ticker = time.NewTicker(time.Duration(dpos.Option.BlockInterval))
		defer ticker.Stop()
	}

	for {
		select {
		case now := <-ticker.C:
			timestamp := dpos.NextSlot(now.UnixNano())
			if err := m.engine.(*dpos.Dpos).CheckValidator(m.uranus, m.uranus.CurrentBlock(), m.coinbase, timestamp); err != nil {
				switch err {
				case dpos.ErrWaitForPrevBlock,
					dpos.ErrMintFutureBlock,
					dpos.ErrInvalidMintBlockTime,
					dpos.ErrInvalidBlockValidator:
					log.Debugf("Failed to mint the block, while %v", err)
				default:
					if _, ok := err.(*mtp.MissingNodeError); !ok {
						log.Errorf("Failed to mint the block, err %v", err)
					}
				}
				continue
			}
			if m.quitCurrentOp != nil {
				close(m.quitCurrentOp)
			}
			m.quitCurrentOp = make(chan struct{})
			go m.mintBlock(timestamp)
		case <-m.stopCh:
			return

		}
	}
}

func (m *UMiner) mintBlock(timestamp int64) {
outer:
	for {
		select {
		case <-m.quitCurrentOp:
			break outer
		default:
		}
		err := m.generateBlock(timestamp)
		if err == nil {
			break outer
		}
		if _, ok := err.(*mtp.MissingNodeError); !ok {
			log.Errorf("Failed to mint the block, err %v", err)
		}
		time.Sleep(time.Duration(dpos.Option.BlockInterval / 10))
	}
}

func (m *UMiner) generateBlock(timestamp int64) error {
	parent, stateDB, err := m.uranus.GetCurrentInfo()
	if err != nil {
		return fmt.Errorf("failed to get current info, %s", err)
	}
	height := parent.BlockHeader().Height
	difficult := m.engine.CalcDifficulty(m.uranus.Config(), uint64(timestamp), parent.BlockHeader())
	header := &types.BlockHeader{
		PreviousHash: parent.Hash(),
		Miner:        m.coinbase,
		Height:       height.Add(height, big.NewInt(1)),
		TimeStamp:    big.NewInt(timestamp),
		GasLimit:     calcGasLimit(parent),
		Difficulty:   difficult,
		ExtraData:    m.extraData,
	}
	var dposContext *types.DposContext = nil
	if _, ok := m.engine.(*dpos.Dpos); ok {
		var err error
		dposContext, err = types.NewDposContextFromProto(stateDB.Database().TrieDB(), parent.BlockHeader().DposContext)
		if err != nil {
			return err
		}
	}
	m.currentWork = NewWork(types.NewBlockWithBlockHeader(header), parent.Height().Uint64(), stateDB, dposContext)

	actions := m.uranus.Actions()

	m.currentWork.applyActions(m.uranus, actions)

	pending, err := m.uranus.Pending()
	if err != nil {
		return fmt.Errorf("Failed to fetch pending transactions, err: %s", err.Error())
	}

	txs := types.NewTransactionsByPriceAndNonce(m.currentWork.signer, pending)
	interval := dpos.Option.BlockInterval
	err = m.currentWork.applyTransactions(m.uranus, txs, timestamp+interval-interval/10)
	if err != nil {
		return fmt.Errorf("failed to apply transaction %s", err)
	}

	header = m.currentWork.Block.BlockHeader()
	header.GasUsed = *m.currentWork.gasUsed
	if atomic.LoadInt32(&m.mining) == 1 {
		block, err := m.engine.Finalize(m.uranus, header, stateDB, m.currentWork.txs, m.currentWork.actions, m.currentWork.receipts, m.currentWork.dposContext)
		if err != nil {
			return err
		}

		block.DposContext = m.currentWork.dposContext
		m.currentWork.Block = block
		result, err := m.engine.Seal(m.uranus, m.currentWork.Block, m.quitCurrentOp, 0, nil)
		if err != nil {
			return err
		}

		if _, err := m.uranus.WriteBlockWithState(result, m.currentWork.receipts, m.currentWork.state); err != nil {
			return err
		}

		log.Infof("Successfully sealed new block number: %v, hash: %v, diff: %v, txs: %v", result.Height(), result.Hash(), result.Difficulty(), len(block.Transactions()))
		m.uranus.PostEvent(feed.BlockAndLogsEvent{Block: result})
		m.mux.Post(feed.NewMinedBlockEvent{
			Block: result,
		})
		return nil
	}
	return nil
}
