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

// this file bounds how much of a potentially huge string a log line or error
// message may quote.

import "fmt"

// AbbreviateMax is how many bytes of a potentially huge string Abbreviate keeps
// before replacing the rest with the string's total length.
//
// The values this bounds are job command lines and RepGroup selectors, both
// entirely user-supplied. A command line big enough to fail exec with E2BIG is
// BY DEFINITION over Linux's MAX_ARG_STRLEN (128KB for a single argv element),
// wr passes the whole command line to the shell as one argument, and every
// runner logs its command line several times per job - which is what took
// production's runner log lines to 1.3MB (measured p99 24,261 bytes, max
// 1,345,498 bytes for a single "reserved a job" line). Past a couple of hundred
// bytes the actionable facts are the command's identity (its job key, from which
// `wr status` yields the whole thing) and its length, not its bytes.
const AbbreviateMax = 200

// Abbreviate renders s for a log line or error message: s itself when it is at
// most AbbreviateMax bytes, otherwise its first AbbreviateMax bytes followed by
// its total byte length.
//
// It is presentation-only - Go strings are immutable, so no caller's value is
// ever altered by abbreviating it, and the underlying Job keeps its full Cmd.
func Abbreviate(s string) string {
	if len(s) <= AbbreviateMax {
		return s
	}

	return fmt.Sprintf("%s[...] (truncated; %d bytes total)", s[:AbbreviateMax], len(s))
}
