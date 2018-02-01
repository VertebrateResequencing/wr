// Copyright © 2017 Genome Research Limited
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

package rp

// This file contains error handling code.

import (
	"fmt"
)

// rp has some typical errors
/* #nosec */
const (
	ErrOverMaximumTokens = "more tokens requested than can ever be supplied"
	ErrShutDown          = "the protector has been shut down"
)

// Error records an error and the operation, request and protector that caused it.
type Error struct {
	Protector string  // the protector's Name
	Op        string  // name of the method
	Request   Receipt // the request's id
	Err       string  // one of our Err constants
}

func (e Error) Error() string {
	return fmt.Sprintf("protector(%s) %s (%s): %s", e.Protector, e.Op, e.Request, e.Err)
}
