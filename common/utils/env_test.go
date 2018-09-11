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

package utils

import (
	"os"
	"testing"
)

func TestString(t *testing.T) {
	result := EnvString("testString", "default")
	EnvParse()

	AssertEquals(t, "default", *result)

	err := os.Setenv("newTestString", "newDefault")
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	result = EnvString("newTestString", "default")
	EnvParse()

	AssertEquals(t, "newDefault", *result)
}