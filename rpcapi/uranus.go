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

package rpcapi

import (
	"context"
	"errors"
	"math/big"

	"github.com/UranusBlockStack/uranus/common/rlp"
	"github.com/UranusBlockStack/uranus/common/utils"
	"github.com/UranusBlockStack/uranus/core/state"
	"github.com/UranusBlockStack/uranus/core/types"
)

// UranusAPI exposes methods for the RPC interface
type UranusAPI struct {
	b Backend
}

// NewUranusAPI creates a new RPC service with methods specific for the uranus.
func NewUranusAPI(b Backend) *UranusAPI {
	return &UranusAPI{b}
}

//SuggestGasPrice Ssugget gas price.
func (u *UranusAPI) SuggestGasPrice(ignore string, reply *big.Int) error {
	reply, err := u.b.SuggestGasPrice(context.Background())
	if err != nil {
		return err
	}
	return nil
}

type GetBalanceArgs struct {
	Address     utils.Address
	BlockHeight BlockHeight
}

// GetBalance returns the amount of wei for the given address in the state of the given block number
func (u *UranusAPI) GetBalance(args GetBalanceArgs, reply *big.Int) error {
	state, err := u.getState(args.BlockHeight)
	if err != nil {
		return err
	}
	*reply = *state.GetBalance(args.Address)
	return nil
}

type GetNonceArgs struct {
	GetBalanceArgs
}

// GetNonce returns nonce for the given address
func (u *UranusAPI) GetNonce(args GetNonceArgs, reply *utils.Uint64) error {
	state, err := u.getState(args.BlockHeight)
	if err != nil {
		return err
	}
	nonce := state.GetNonce(args.Address)
	*reply = (utils.Uint64)(nonce)
	return nil
}

type GetCodeArgs struct {
	GetBalanceArgs
}

// GetCode returns code for the given address
func (u *UranusAPI) GetCode(args GetCodeArgs, reply *utils.Bytes) error {
	state, err := u.getState(args.BlockHeight)
	if err != nil {
		return err
	}
	code := state.GetCode(args.Address)
	*reply = code
	return nil
}

// SendTxArgs represents the arguments to sumbit a new transaction into the transaction pool.
type SendTxArgs struct {
	From       utils.Address
	To         *utils.Address
	Gas        *utils.Uint64
	GasPrice   *utils.Big
	Value      *utils.Big
	Nonce      *utils.Uint64
	Data       *utils.Bytes
	Passphrase string
}

// check is a helper function that fills in default values for unspecified tx fields.
func (args *SendTxArgs) check(ctx context.Context, b Backend) error {
	if args.Gas == nil {
		args.Gas = new(utils.Uint64)
		*(*uint64)(args.Gas) = 90000
	}
	if args.GasPrice == nil {
		price, err := b.SuggestGasPrice(ctx)
		if err != nil {
			return err
		}
		args.GasPrice = (*utils.Big)(price)
	}
	if args.Value == nil {
		args.Value = new(utils.Big)
	}
	if args.Nonce == nil {
		nonce, err := b.GetPoolNonce(ctx, args.From)
		if err != nil {
			return err
		}
		args.Nonce = (*utils.Uint64)(&nonce)
	}

	if args.To == nil {
		// Contract creation
		var input []byte
		if args.Data != nil {
			input = *args.Data
		}
		if len(input) == 0 {
			return errors.New(`contract creation without any data provided`)
		}
	}
	return nil
}

func (args *SendTxArgs) toTransaction() *types.Transaction {
	var input []byte
	if args.Data != nil {
		input = *args.Data
	}
	if args.To == nil {
		return types.NewTransaction(uint64(*args.Nonce), utils.Address{}, (*big.Int)(args.Value), uint64(*args.Gas), (*big.Int)(args.GasPrice), input)
	}
	return types.NewTransaction(uint64(*args.Nonce), *args.To, (*big.Int)(args.Value), uint64(*args.Gas), (*big.Int)(args.GasPrice), input)
}

// SignAndSendTransaction sign and send transaction .
func (u *UranusAPI) SignAndSendTransaction(args SendTxArgs, reply *utils.Hash) error {
	if err := args.check(context.Background(), u.b); err != nil {
		return err
	}

	tx, err := u.b.SignTx(args.From, args.toTransaction(), args.Passphrase)
	if err != nil {
		return err
	}

	hash, err := submitTransaction(context.Background(), u.b, tx)
	if err != nil {
		return err
	}

	*reply = hash
	return nil
}

// SendRawTransaction will add the signed transaction to the transaction pool.
func (u *UranusAPI) SendRawTransaction(encodedTx utils.Bytes, reply *utils.Hash) error {
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(encodedTx, tx); err != nil {
		return err
	}
	hash, err := submitTransaction(context.Background(), u.b, tx)
	if err != nil {
		return err
	}
	*reply = hash
	return nil
}

func (u *UranusAPI) getState(height BlockHeight) (*state.StateDB, error) {
	block, err := u.b.BlockByHeight(context.Background(), height)
	if err != nil {
		return nil, err
	}
	return u.b.BlockChain().StateAt(block.StateRoot())
}
