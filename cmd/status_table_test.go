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
	"bytes"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestStatusTableUnicodeCells(t *testing.T) {
	Convey("status table cells truncate non-ASCII text by rune", t, func() {
		column := statusTableColumn{width: 4}

		var output bytes.Buffer

		writeStatusTableCell(&output, column, "éabcd")

		So(output.String(), ShouldEqual, "é...")
		So(utf8.ValidString(output.String()), ShouldBeTrue)
	})
}

func TestStatusTableFormatErrors(t *testing.T) {
	Convey("wr status table reports missing FIELD:width syntax", t, func() {
		t.Setenv(statusFormatEnv, statusTableStatusFieldName)

		err := writeStatusTable(io.Discard, nil)

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "FIELD:width")
		So(err.Error(), ShouldNotContainSubstring, errStatusFormatEmpty.Error())
	})

	Convey("wr status table reports empty widths as bad positive widths", t, func() {
		t.Setenv(statusFormatEnv, statusTableStatusFieldName+":")

		err := writeStatusTable(io.Discard, nil)

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, errStatusFormatBadWidth.Error())
		So(err.Error(), ShouldNotContainSubstring, errStatusFormatEmpty.Error())
	})
}

func TestStatusTableRows(t *testing.T) {
	Convey("wr status table renders reserved jobs as running", t, func() {
		t.Setenv(statusFormatEnv, "status:9 count:5")

		jobs := []*jobqueue.Job{
			{State: jobqueue.JobStateReserved},
		}

		var output bytes.Buffer

		err := writeStatusTable(&output, jobs)

		So(err, ShouldBeNil)
		So(output.String(), ShouldContainSubstring, "running")
		So(output.String(), ShouldNotContainSubstring, "reserved")
	})

	Convey("wr status table repeats the limited status group total on each row", t, func() {
		t.Setenv(statusFormatEnv, "command:9 status:9 count:5")

		jobs := []*jobqueue.Job{
			{Cmd: "first", State: jobqueue.JobStateBuried, Exitcode: 1, FailReason: jobqueue.FailReasonExit},
			{
				Cmd:        "second",
				State:      jobqueue.JobStateBuried,
				Exitcode:   1,
				FailReason: jobqueue.FailReasonExit,
				Similar:    2,
			},
		}

		var output bytes.Buffer

		err := writeStatusTable(&output, jobs)
		lines := nonEmptyStatusLines(output.String())

		So(err, ShouldBeNil)
		So(lines, ShouldHaveLength, 3)
		So(lines[1], ShouldContainSubstring, "first")
		So(lines[1], ShouldContainSubstring, "buried")
		So(lines[1], ShouldContainSubstring, "4")
		So(lines[2], ShouldContainSubstring, "second")
		So(lines[2], ShouldContainSubstring, "buried")
		So(lines[2], ShouldContainSubstring, "4")
	})

	Convey("wr status table renders never-seen dep-group waits as waiting-deps", t, func() {
		t.Setenv(statusFormatEnv, "status:12 count:5")

		jobs := []*jobqueue.Job{
			{
				State:               jobqueue.JobStateDependent,
				WaitingForDepGroups: []string{"future"},
			},
		}

		var output bytes.Buffer

		err := writeStatusTable(&output, jobs)

		So(err, ShouldBeNil)
		So(output.String(), ShouldContainSubstring, "waiting-deps")
		So(output.String(), ShouldNotContainSubstring, "dependent")
	})
}

func TestStatusLimitHelp(t *testing.T) {
	Convey("wr status --limit help describes grouped outputs", t, func() {
		flag := statusCmd.Flags().Lookup("limit")

		So(flag, ShouldNotBeNil)
		So(flag.Usage, ShouldContainSubstring, "grouped outputs")
		So(flag.Usage, ShouldContainSubstring, "details")
		So(flag.Usage, ShouldContainSubstring, "table")
	})
}

func TestStatusTableOutputHelp(t *testing.T) {
	Convey("wr status table output help describes rows without promising exactly one per status group", t, func() {
		So(statusCmd.Long, ShouldContainSubstring, `"table" outputs aligned rows`)
		So(statusCmd.Long, ShouldContainSubstring, "--limit")
		So(statusCmd.Long, ShouldContainSubstring, "WR_STATUS_FORMAT")
		So(statusCmd.Long, ShouldNotContainSubstring, "one aligned row per status group")
	})

	Convey("wr status help lists valid WR_STATUS_FORMAT fields", t, func() {
		help := compactWhitespace(commandHelpForTest(t, statusCmd))

		So(help, ShouldContainSubstring, "Valid WR_STATUS_FORMAT FIELD names:")

		for _, fields := range []string{
			"command/cmd",
			"id/jobid/key",
			"status/state",
			"attempts/tries",
			"host",
			"reqgroup/requirements/requirementsgroup",
			"count/similar",
		} {
			So(help, ShouldContainSubstring, fields)
		}
	})

	Convey("wr status help wraps the WR_STATUS_FORMAT FIELD list within 80 columns", t, func() {
		lines := strings.Split(commandHelpForTest(t, statusCmd), "\n")
		checkFieldHelpLine := false
		checked := 0

		for _, line := range lines {
			if strings.Contains(line, "Valid WR_STATUS_FORMAT FIELD names:") {
				checkFieldHelpLine = true

				continue
			}

			if !checkFieldHelpLine {
				continue
			}

			So(len(line), ShouldBeLessThanOrEqualTo, 80)

			checked++

			if strings.Contains(line, "Field names are case-insensitive") {
				break
			}
		}

		So(checked, ShouldBeGreaterThan, 0)
	})
}

func TestStatusPlainOutputHelp(t *testing.T) {
	Convey("wr status plain output help lists dependent jobs", t, func() {
		So(statusCmd.Long, ShouldContainSubstring, `"plain" outputs 2 tab separated columns`)
		So(statusCmd.Long, ShouldContainSubstring, "dependent, suspended")
	})
}

func TestStatusOutputRetrievalNeeds(t *testing.T) {
	Convey("wr status only retrieves stdout and stderr for outputs that render them", t, func() {
		for _, tc := range []struct {
			format string
			want   bool
		}{
			{statusOutputFormatCounts, false},
			{statusOutputFormatCountsAlias, false},
			{statusOutputFormatSummary, false},
			{statusOutputFormatSummaryAlias, false},
			{statusOutputFormatDetails, true},
			{statusOutputFormatDetailsAlias, true},
			{statusOutputFormatPlain, false},
			{statusOutputFormatPlainAlias, false},
			{statusOutputFormatTable, false},
			{statusOutputFormatTableAlias, false},
			{statusOutputFormatJSON, true},
			{statusOutputFormatJSONAlias, true},
		} {
			So(statusOutputGetsStd(tc.format), ShouldEqual, tc.want)
		}
	})

	Convey("wr status only applies --env to details output", t, func() {
		for _, tc := range []struct {
			format string
			want   bool
		}{
			{statusOutputFormatCounts, false},
			{statusOutputFormatCountsAlias, false},
			{statusOutputFormatSummary, false},
			{statusOutputFormatSummaryAlias, false},
			{statusOutputFormatDetails, true},
			{statusOutputFormatDetailsAlias, true},
			{statusOutputFormatPlain, false},
			{statusOutputFormatPlainAlias, false},
			{statusOutputFormatTable, false},
			{statusOutputFormatTableAlias, false},
			{statusOutputFormatJSON, false},
			{statusOutputFormatJSONAlias, false},
		} {
			So(statusOutputGetsEnv(tc.format), ShouldEqual, tc.want)
		}
	})
}
