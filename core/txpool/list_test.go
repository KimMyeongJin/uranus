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

package txpool

import (
	"math/rand"
	"testing"

	"github.com/UranusBlockStack/uranus/common/crypto"
	"github.com/UranusBlockStack/uranus/common/utils"
	"github.com/UranusBlockStack/uranus/core/types"
)

func TestTxListAdd(t *testing.T) {
	key, _ := crypto.GenerateKey()

	txs := make(types.Transactions, 1024)
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
	}
	list := newTxList(true)
	for _, v := range rand.Perm(len(txs)) {
		list.Add(txs[v], DefaultTxPoolConfig.PriceBump)
	}
	utils.AssertEquals(t, len(list.txs.items), len(txs))
	for _, tx := range txs {
		utils.AssertEquals(t, list.txs.items[tx.Nonce()], tx)
	}
}
