//go:build !race

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

package jobqueue

import (
	"context"
	"flag"
	"os"
)

// dispatchSubprocessMode does the work of a --runnermode or --servermode child
// and exits the process, before any test is allowed to run.
//
// It exists because these children are copies of this test binary, so without
// it a child runs every test that does not itself guard on runnermode or
// servermode. Those tests start their own managers, and jobqueueTestInit only
// isolates ports and the manager dir when NOT in one of these modes (children
// are meant to inherit the parent's), so each of those managers falls back to
// the default config - the user's real manager port. One runner child was
// measured holding 16498 sockets on it, listening and connecting to itself at
// ~250 connections/second. See .docs/bugfixes/260828-4.md BUG 12.
//
// That was fixed by passing -test.run on each child's cmdline, which is still
// done: it keeps each child's cmdline consistent, which crash recovery relies
// on to match a recovered runner, and it remains a second layer of defence.
// This is the layer a newly added call site cannot forget to apply.
func dispatchSubprocessMode() {
	flag.Parse()

	ctx := context.Background()

	switch {
	case runnermode:
		runner(ctx)
	case servermode:
		runServer(ctx)
	default:
		return
	}

	os.Exit(0)
}
