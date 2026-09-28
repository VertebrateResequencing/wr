/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
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

package internal

import (
	"errors"
	"fmt"
	"math"
	"syscall"
)

// ErrInvalidPid is returned by SignalPid for a pid that does not name a single
// process.
var ErrInvalidPid = errors.New("not a valid pid")

// SignalPid sends sig to pid (signal 0 probes whether it exists), or returns
// ErrInvalidPid without signalling anything if pid is not ValidPid.
func SignalPid(pid int, sig syscall.Signal) error {
	if !ValidPid(pid) {
		return fmt.Errorf("%w: %d", ErrInvalidPid, pid)
	}

	return syscall.Kill(pid, sig)
}

// ValidPid reports whether pid names a single process. kill(2) treats 0 as the
// caller's own process group, -1 as every process the caller may signal, and
// any other negative number as a process group, so a pid read from a file or
// reported by another process must pass this before it is signalled or probed.
func ValidPid(pid int) bool {
	return pid > 0 && pid <= math.MaxInt32
}
