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
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/VertebrateResequencing/wr/jobqueue"
)

const (
	statusFormatEnv                  = "WR_STATUS_FORMAT"
	defaultStatusTableColumns        = "command:36 id:32 status:12 attempts:8 host:16 reqgroup:18 count:5"
	statusTableColumnSeparator       = "  "
	statusTableDefaultTruncateMarker = "..."
	statusTableStatusFieldName       = "status"
	statusDisplayStateWaitingDeps    = jobqueue.JobState("waiting-deps")
	statusOutputFormatCounts         = "counts"
	statusOutputFormatCountsAlias    = "c"
	statusOutputFormatSummary        = "summary"
	statusOutputFormatSummaryAlias   = "s"
	statusOutputFormatDetails        = "details"
	statusOutputFormatDetailsAlias   = "d"
	statusOutputFormatPlain          = "plain"
	statusOutputFormatPlainAlias     = "p"
	statusOutputFormatTable          = "table"
	statusOutputFormatTableAlias     = "t"
	statusOutputFormatJSON           = "json"
	statusOutputFormatJSONAlias      = "j"
)

type statusTableField struct {
	header string
	right  bool
	value  func(statusTableRow) string
}

type statusTableFieldOption struct {
	names []string
	field statusTableField
}

var (
	errStatusFormatEmpty    = errors.New("no fields supplied")
	errStatusFormatBadField = errors.New("field must use FIELD:width syntax")
	errStatusFormatBadWidth = errors.New("field width must be a positive integer")
	errStatusFormatUnknown  = errors.New("unknown field")
	statusTableCommandField = statusTableField{
		header: "Command",
		value:  func(row statusTableRow) string { return row.job.Cmd },
	}
	statusTableIDField = statusTableField{
		header: "ID",
		value:  func(row statusTableRow) string { return row.job.Key() },
	}
	statusTableStatusField = statusTableField{
		header: "Status",
		value:  func(row statusTableRow) string { return string(row.displayState) },
	}
	statusTableAttemptsField = statusTableField{
		header: "Attempts",
		right:  true,
		value:  func(row statusTableRow) string { return strconv.FormatUint(uint64(row.job.Attempts), 10) },
	}
	statusTableHostField = statusTableField{
		header: "Host",
		value:  statusTableRowHost,
	}
	statusTableReqGroupField = statusTableField{
		header: "Requirements group",
		value:  func(row statusTableRow) string { return row.job.ReqGroup },
	}
	statusTableCountField = statusTableField{
		header: "Count",
		right:  true,
		value:  func(row statusTableRow) string { return strconv.Itoa(row.count) },
	}
	statusTableFieldOptions = []statusTableFieldOption{
		{names: []string{"command", "cmd"}, field: statusTableCommandField},
		{names: []string{"id", "jobid", "key"}, field: statusTableIDField},
		{names: []string{statusTableStatusFieldName, "state"}, field: statusTableStatusField},
		{names: []string{"attempts", "tries"}, field: statusTableAttemptsField},
		{names: []string{"host"}, field: statusTableHostField},
		{names: []string{"reqgroup", "requirements", "requirementsgroup"}, field: statusTableReqGroupField},
		{names: []string{"count", "similar"}, field: statusTableCountField},
	}
	statusTableFieldsByName = newStatusTableFieldsByName(statusTableFieldOptions)
)

func statusTableFormatFieldsHelp() string {
	groups := make([]string, 0, len(statusTableFieldOptions))
	for _, option := range statusTableFieldOptions {
		groups = append(groups, strings.Join(option.names, "/"))
	}

	return strings.Join(groups, ", ")
}

func statusOutputShowsAlerts(format string) bool {
	switch format {
	case statusOutputFormatCounts, statusOutputFormatCountsAlias,
		statusOutputFormatJSON, statusOutputFormatJSONAlias,
		statusOutputFormatPlain, statusOutputFormatPlainAlias:
		return false
	default:
		return true
	}
}

type statusTableRow struct {
	job          *jobqueue.Job
	displayState jobqueue.JobState
	count        int
}

func statusTableRowHost(row statusTableRow) string {
	return statusTableHost(row.job)
}

func statusTableHost(job *jobqueue.Job) string {
	switch {
	case job.Host != "":
		return job.Host
	case job.HostID != "":
		return job.HostID
	case job.HostIP != "":
		return job.HostIP
	default:
		return ""
	}
}

func statusOutputGetsEnv(format string) bool {
	return format == statusOutputFormatDetails || format == statusOutputFormatDetailsAlias
}

func newStatusTableRows(jobs []*jobqueue.Job) []statusTableRow {
	totals := statusTableGroupTotals(jobs)
	rows := make([]statusTableRow, 0, len(jobs))

	for _, job := range jobs {
		key := statusTableGroupKeyForJob(job)
		rows = append(rows, statusTableRow{
			job:          job,
			displayState: key.state,
			count:        totals[key],
		})
	}

	return rows
}

func statusTableGroupTotals(jobs []*jobqueue.Job) map[statusTableGroupKey]int {
	totals := make(map[statusTableGroupKey]int)

	for _, job := range jobs {
		totals[statusTableGroupKeyForJob(job)] += 1 + job.Similar
	}

	return totals
}

func statusTableGroupKeyForJob(job *jobqueue.Job) statusTableGroupKey {
	return statusTableGroupKey{
		state:      statusTableDisplayState(job),
		exitcode:   job.Exitcode,
		failReason: job.FailReason,
	}
}

func statusTableDisplayState(job *jobqueue.Job) jobqueue.JobState {
	state := statusDisplayState(job)
	if state == jobqueue.JobStateReserved {
		return jobqueue.JobStateRunning
	}

	return state
}

func statusDisplayState(job *jobqueue.Job) jobqueue.JobState {
	if len(job.WaitingForDepGroups) > 0 {
		return statusDisplayStateWaitingDeps
	}

	return job.State
}

func statusTableFieldForName(name string) (statusTableField, error) {
	if strings.TrimSpace(name) == "" {
		return statusTableField{}, errStatusFormatBadField
	}

	field, found := statusTableFieldsByName[normaliseStatusTableFieldName(name)]
	if !found {
		return statusTableField{}, errStatusFormatUnknown
	}

	return field, nil
}

func newStatusTableFieldsByName(options []statusTableFieldOption) map[string]statusTableField {
	fields := make(map[string]statusTableField)

	for _, option := range options {
		for _, name := range option.names {
			fields[normaliseStatusTableFieldName(name)] = option.field
		}
	}

	return fields
}

type statusTableGroupKey struct {
	state      jobqueue.JobState
	exitcode   int
	failReason string
}

type statusTableColumn struct {
	field statusTableField
	width int
}

func parseStatusTableColumn(part string) (statusTableColumn, error) {
	name, widthText, found := strings.Cut(part, ":")
	if !found || strings.TrimSpace(name) == "" {
		return statusTableColumn{}, errStatusFormatBadField
	}

	width, err := parseStatusTableColumnWidth(widthText)
	if err != nil {
		return statusTableColumn{}, err
	}

	field, err := statusTableFieldForName(name)
	if err != nil {
		return statusTableColumn{}, err
	}

	if len(field.header) > width {
		width = len(field.header)
	}

	return statusTableColumn{field: field, width: width}, nil
}

func writeStatusTable(w io.Writer, jobs []*jobqueue.Job) error {
	columns, err := parseStatusTableColumns(os.Getenv(statusFormatEnv))
	if err != nil {
		return err
	}

	writeStatusTableRow(w, columns, func(column statusTableColumn) string {
		return column.field.header
	})

	for _, row := range newStatusTableRows(jobs) {
		writeStatusTableRow(w, columns, func(column statusTableColumn) string {
			return column.field.value(row)
		})
	}

	return nil
}

func parseStatusTableColumns(format string) ([]statusTableColumn, error) {
	if strings.TrimSpace(format) == "" {
		format = defaultStatusTableColumns
	}

	parts := strings.Fields(format)
	if len(parts) == 0 {
		return nil, errStatusFormatEmpty
	}

	columns := make([]statusTableColumn, 0, len(parts))
	for _, part := range parts {
		column, err := parseStatusTableColumn(part)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", part, err)
		}

		columns = append(columns, column)
	}

	return columns, nil
}

func writeStatusTableRow(w io.Writer, columns []statusTableColumn, value func(statusTableColumn) string) {
	for n, column := range columns {
		if n > 0 {
			fmt.Fprint(w, statusTableColumnSeparator)
		}

		writeStatusTableCell(w, column, value(column))
	}

	fmt.Fprintln(w)
}

func writeStatusTableCell(w io.Writer, column statusTableColumn, value string) {
	value = fitStatusTableValue(value, column.width)
	if column.field.right {
		fmt.Fprintf(w, "%*s", column.width, value)

		return
	}

	fmt.Fprintf(w, "%-*s", column.width, value)
}

func fitStatusTableValue(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}

	truncateMarkerRunes := []rune(statusTableDefaultTruncateMarker)
	if width <= len(truncateMarkerRunes) {
		return string(runes[:width])
	}

	return string(runes[:width-len(truncateMarkerRunes)]) + statusTableDefaultTruncateMarker
}

func parseStatusTableColumnWidth(widthText string) (int, error) {
	widthText = strings.TrimSpace(widthText)
	if widthText == "" {
		return 0, errStatusFormatBadWidth
	}

	width, err := strconv.Atoi(widthText)
	if err != nil || width <= 0 {
		return 0, errStatusFormatBadWidth
	}

	return width, nil
}

func statusOutputUsesGroupedJobs(format string) bool {
	switch format {
	case statusOutputFormatDetails, statusOutputFormatDetailsAlias,
		statusOutputFormatJSON, statusOutputFormatJSONAlias,
		statusOutputFormatTable, statusOutputFormatTableAlias:
		return true
	default:
		return false
	}
}

func statusOutputGetsStd(format string) bool {
	switch format {
	case statusOutputFormatDetails, statusOutputFormatDetailsAlias,
		statusOutputFormatJSON, statusOutputFormatJSONAlias:
		return true
	default:
		return false
	}
}

func normaliseStatusTableFieldName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")

	return name
}
