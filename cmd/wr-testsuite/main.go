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

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/VertebrateResequencing/wr/internal/testsuite"
)

const (
	expectedArgs = 2
	exitFailure  = 1
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) != expectedArgs {
		_, _ = fmt.Fprintln(os.Stderr, "usage: wr-testsuite test|race")

		return exitFailure
	}

	mode, err := testsuite.ParseMode(os.Args[1])
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		return exitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := testsuite.Run(ctx, os.Stdout, os.Stderr, mode); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		return exitFailure
	}

	return 0
}
