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

package params

import (
	"math/big"
)

var (
	GenesisCandidate = "0x970e8128ab834e8eac17ab8e3812f010678cf791"
	MinStartQuantity = big.NewInt(100)

	// MaxVotes Maximum size of dpos Delegate votes.
	MaxVotes uint64 = 30

	// DelayDuration the redeem transacion delay Duration.
	DelayDuration = big.NewInt(72 * 3600)
)
