/*******************************************************************************
 * Copyright (c) 2016-2019 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

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
