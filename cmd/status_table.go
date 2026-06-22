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
	defaultStatusTableColumns        = "command:36 id:32 status:10 attempts:8 host:16 reqgroup:18 count:5"
	statusTableColumnSeparator       = "  "
	statusTableDefaultTruncateMarker = "..."
	statusTableStatusFieldName       = "status"
)

type statusTableField struct {
	header string
	right  bool
	value  func(*jobqueue.Job) string
}

var (
	errStatusFormatEmpty    = errors.New("no fields supplied")
	errStatusFormatBadWidth = errors.New("field width must be a positive integer")
	errStatusFormatUnknown  = errors.New("unknown field")
	statusTableCommandField = statusTableField{
		header: "Command",
		value:  func(job *jobqueue.Job) string { return job.Cmd },
	}
	statusTableIDField = statusTableField{
		header: "ID",
		value:  func(job *jobqueue.Job) string { return job.Key() },
	}
	statusTableStatusField = statusTableField{
		header: "Status",
		value:  func(job *jobqueue.Job) string { return string(job.State) },
	}
	statusTableAttemptsField = statusTableField{
		header: "Attempts",
		right:  true,
		value:  func(job *jobqueue.Job) string { return strconv.FormatUint(uint64(job.Attempts), 10) },
	}
	statusTableHostField = statusTableField{
		header: "Host",
		value:  statusTableHost,
	}
	statusTableReqGroupField = statusTableField{
		header: "Requirements group",
		value:  func(job *jobqueue.Job) string { return job.ReqGroup },
	}
	statusTableCountField = statusTableField{
		header: "Count",
		right:  true,
		value:  func(job *jobqueue.Job) string { return strconv.Itoa(1 + job.Similar) },
	}
	statusTableFieldsByName = map[string]statusTableField{
		"command":                  statusTableCommandField,
		"cmd":                      statusTableCommandField,
		"id":                       statusTableIDField,
		"jobid":                    statusTableIDField,
		"key":                      statusTableIDField,
		statusTableStatusFieldName: statusTableStatusField,
		"state":                    statusTableStatusField,
		"attempts":                 statusTableAttemptsField,
		"tries":                    statusTableAttemptsField,
		"host":                     statusTableHostField,
		"reqgroup":                 statusTableReqGroupField,
		"requirements":             statusTableReqGroupField,
		"requirementsgroup":        statusTableReqGroupField,
		"count":                    statusTableCountField,
		"similar":                  statusTableCountField,
	}
)

func statusOutputShowsAlerts(format string) bool {
	return format != "json" && format != "j" && format != "plain" && format != "p"
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

func statusTableFieldForName(name string) (statusTableField, error) {
	if strings.TrimSpace(name) == "" {
		return statusTableField{}, errStatusFormatEmpty
	}

	field, found := statusTableFieldsByName[normaliseStatusTableFieldName(name)]
	if !found {
		return statusTableField{}, errStatusFormatUnknown
	}

	return field, nil
}

type statusTableColumn struct {
	field statusTableField
	width int
}

func parseStatusTableColumn(part string) (statusTableColumn, error) {
	name, widthText, found := strings.Cut(part, ":")
	if !found {
		return statusTableColumn{}, errStatusFormatEmpty
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

	for _, job := range jobs {
		writeStatusTableRow(w, columns, func(column statusTableColumn) string {
			return column.field.value(job)
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
	if len(value) <= width {
		return value
	}

	if width <= len(statusTableDefaultTruncateMarker) {
		return value[:width]
	}

	return value[:width-len(statusTableDefaultTruncateMarker)] + statusTableDefaultTruncateMarker
}

func parseStatusTableColumnWidth(widthText string) (int, error) {
	if strings.TrimSpace(widthText) == "" {
		return 0, errStatusFormatEmpty
	}

	width, err := strconv.Atoi(widthText)
	if err != nil || width <= 0 {
		return 0, errStatusFormatBadWidth
	}

	return width, nil
}

func statusOutputUsesGroupedJobs(format string) bool {
	return strings.HasPrefix(format, "d") || strings.HasPrefix(format, "j") || statusOutputIsTable(format)
}

func statusOutputGetsStd(format string) bool {
	return !statusOutputIsTable(format)
}

func statusOutputIsTable(format string) bool {
	return format == "table" || format == "t"
}

func normaliseStatusTableFieldName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")

	return name
}
