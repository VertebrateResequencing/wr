// Copyright © 2019 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package limiter

// This file contains error handling code.

import (
	"fmt"
)

// limiter has some typical errors
const (
	ErrNotIncremented = "decrement attempted on a limit group that had not been incremented"
	ErrAtLimit        = "increment attempted on a limit group already at its limit"
)

// Error records an error and the operation, request and protector that caused it.
type Error struct {
	Group string // the limit's Name
	Op    string // name of the method
	Err   string // one of our Err constants
}

func (e Error) Error() string {
	return fmt.Sprintf("limiter(%s) %s(): %s", e.Group, e.Op, e.Err)
}
