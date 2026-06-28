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

package cmd

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

const (
	hoursPerDay  = 24
	daysPerWeek  = 7
	dayDuration  = hoursPerDay * time.Hour
	weekDuration = daysPerWeek * dayDuration
)

// errEmptyRecentDuration is returned by parseRecentDuration when given an empty
// string.
var errEmptyRecentDuration = errors.New(
	"--recent needs a duration, eg. 1d (days), 1w (weeks) or a Go duration " +
		"such as 36h or 90m",
)

// errNonPositiveRecentDuration is returned by parseRecentDuration when the
// parsed duration is zero or negative; the --recent window must be positive.
var errNonPositiveRecentDuration = errors.New(
	"--recent needs a positive duration window; accepted units are d (days), " +
		"w (weeks) and Go duration units such as h, m, s",
)

// parseRecentDuration parses a duration like time.ParseDuration but also takes
// a single trailing convenience unit d (days = 24h) or w (weeks = 7*24h), eg.
// "1d", "2w", "36h", "90m". A bare number, an empty string, a zero/negative
// duration, or any unparseable value returns an error.
//
// The trailing d/w unit only applies when the prefix (everything before the
// final character) parses as a non-negative, finite float; otherwise the whole
// string is passed to time.ParseDuration. Only a single trailing convenience
// unit is supported, so combined values like "1d12h" are not accepted and
// error.
func parseRecentDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, errEmptyRecentDuration
	}

	d, err := parseRecentDurationValue(s)
	if err != nil {
		return 0, err
	}

	if d <= 0 {
		return 0, errNonPositiveRecentDuration
	}

	return d, nil
}

// parseRecentDurationValue does the unit conversion for parseRecentDuration,
// without the positive-window check. It tries the d/w convenience units first,
// then falls back to time.ParseDuration.
func parseRecentDurationValue(s string) (time.Duration, error) {
	if mult, ok := recentConvenienceUnit(s[len(s)-1]); ok {
		if d, ok := parseRecentFloatUnit(s[:len(s)-1], mult); ok {
			return d, nil
		}
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--recent could not parse %q as a duration; "+
			"accepted units are d (days), w (weeks) and Go duration units such "+
			"as h, m, s (eg. 1d, 2w, 36h, 90m): %w", s, err)
	}

	return d, nil
}

// recentConvenienceUnit returns the time.Duration multiplier for a trailing
// convenience unit byte (d or w), and whether it was a recognised unit.
func recentConvenienceUnit(unit byte) (time.Duration, bool) {
	switch unit {
	case 'd':
		return dayDuration, true
	case 'w':
		return weekDuration, true
	default:
		return 0, false
	}
}

// parseRecentFloatUnit parses prefix as a non-negative, finite float and
// multiplies it by mult to get a duration. It reports false (not an error) if
// the prefix is not such a float, so the caller can fall back to
// time.ParseDuration.
func parseRecentFloatUnit(prefix string, mult time.Duration) (time.Duration, bool) {
	f, err := strconv.ParseFloat(prefix, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f < 0 {
		return 0, false
	}

	// f is finite and non-negative here, so ns is too. A non-negative float64
	// converts safely to an int64 nanosecond Duration only while it stays below
	// 2^63; float64(math.MaxInt64) rounds to exactly 2^63, so reject ns at or
	// above it and let the caller fall back to time.ParseDuration (which errors
	// cleanly) rather than rely on the implementation-defined float->int cast.
	ns := f * float64(mult)
	if ns >= float64(math.MaxInt64) {
		return 0, false
	}

	return time.Duration(ns), true
}
