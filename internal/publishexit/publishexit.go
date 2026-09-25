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

// Package publishexit holds how a starting jobqueue server ends the process
// when it cannot make itself reachable. It is internal so that tests anywhere
// in this module can replace the exit, while library callers of jobqueue cannot
// turn it off.
package publishexit

import "os"

// Exit is what a starting server calls when it cannot write its token file or
// bind its manager port. It is os.Exit except while a test has replaced it.
//
//nolint:gochecknoglobals // deliberate test seam
var Exit = os.Exit

// Set replaces Exit with exit and returns a func that restores the previous
// one. With a replacement that returns, publication gives up, the server's
// Serving() stays open, and the test can Stop the server. Call it only while no
// server is starting.
func Set(exit func(code int)) (restore func()) {
	previous := Exit
	Exit = exit

	return func() { Exit = previous }
}
