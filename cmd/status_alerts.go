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
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
)

type schedulerAlertsGetter interface {
	GetSchedulerAlerts() (*jobqueue.SchedulerAlerts, error)
}

func writeStatusAlerts(w io.Writer, jq schedulerAlertsGetter, format string) {
	if !statusOutputShowsAlerts(format) {
		return
	}

	alerts, err := jq.GetSchedulerAlerts()
	if err != nil {
		warn("failed to retrieve scheduler alerts: %s", err)

		return
	}

	writeStatusAlertsFooter(w, alerts)
}

func writeStatusAlertsFooter(w io.Writer, alerts *jobqueue.SchedulerAlerts) {
	if alerts == nil || (len(alerts.Issues) == 0 && len(alerts.BadServers) == 0) {
		return
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Scheduler alerts:")
	writeStatusIssueAlerts(w, alerts.Issues)
	writeStatusBadServerAlerts(w, alerts.BadServers)
}

func writeStatusIssueAlerts(w io.Writer, issues []*jobqueue.SchedulerIssue) {
	for _, issue := range sortedStatusIssues(issues) {
		if issue == nil {
			continue
		}

		writeStatusIssueAlert(w, issue)
	}
}

func sortedStatusIssues(issues []*jobqueue.SchedulerIssue) []*jobqueue.SchedulerIssue {
	issues = slices.Clone(issues)
	slices.SortFunc(issues, func(a, b *jobqueue.SchedulerIssue) int {
		if a == nil || b == nil {
			return compareNilStatusAlerts(a == nil, b == nil)
		}

		return strings.Compare(a.Msg, b.Msg)
	})

	return issues
}

func writeStatusIssueAlert(w io.Writer, issue *jobqueue.SchedulerIssue) {
	fmt.Fprintf(w, "- Scheduler Issue: %s", issue.Msg)
	fmt.Fprint(w, statusIssueAlertSuffix(issue))
	fmt.Fprintln(w)
}

func statusIssueAlertSuffix(issue *jobqueue.SchedulerIssue) string {
	switch {
	case issue.LastDate > 0 && issue.Count > 1:
		return fmt.Sprintf(" (reported at %s; first reported at %s; reported %d times)",
			formatStatusAlertTime(issue.LastDate), formatStatusAlertTime(issue.FirstDate), issue.Count)
	case issue.LastDate > 0:
		return fmt.Sprintf(" (reported at %s)", formatStatusAlertTime(issue.LastDate))
	case issue.Count > 1:
		return fmt.Sprintf(" (reported %d times)", issue.Count)
	default:
		return ""
	}
}

func writeStatusBadServerAlerts(w io.Writer, badServers []*jobqueue.BadServer) {
	for _, server := range sortedStatusBadServers(badServers) {
		if server == nil {
			continue
		}

		writeStatusBadServerAlert(w, server)
	}
}

func sortedStatusBadServers(badServers []*jobqueue.BadServer) []*jobqueue.BadServer {
	badServers = slices.Clone(badServers)
	slices.SortFunc(badServers, func(a, b *jobqueue.BadServer) int {
		if a == nil || b == nil {
			return compareNilStatusAlerts(a == nil, b == nil)
		}

		return strings.Compare(a.ID, b.ID)
	})

	return badServers
}

func compareNilStatusAlerts(aNil, bNil bool) int {
	switch {
	case aNil && bNil:
		return 0
	case aNil:
		return 1
	default:
		return -1
	}
}

func writeStatusBadServerAlert(w io.Writer, server *jobqueue.BadServer) {
	fmt.Fprintf(w, "- Bad server: %s (%s, %s) %s",
		server.Name, server.ID, server.IP, statusBadServerAlertState(server))

	if server.Date > 0 {
		fmt.Fprintf(w, "; reported at %s", formatStatusAlertTime(server.Date))
	}

	fmt.Fprintln(w)
}

func statusBadServerAlertState(server *jobqueue.BadServer) string {
	if server.Problem == "" {
		return "might be dead"
	}

	return "is no longer usable; problem: " + server.Problem
}

//nolint:gosmopolitan // wr status intentionally renders alert times in the user's local timezone.
func formatStatusAlertTime(unixSeconds int64) string {
	return time.Unix(unixSeconds, 0).Local().Format(shortTimeFormat)
}
