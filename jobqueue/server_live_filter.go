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

// liveJobGroup is the parts of jobGroup's key, so that grouping every live job
// does not cost a formatted string each.
type liveJobGroup struct {
	state      JobState
	exitCode   int
	failReason string
}

// newLiveJobFilter returns a filter for jobs matching repGroup (any, if it is
// blank or match is exact, since then the caller only walks that RepGroup's
// keys) that opts would keep.
func (s *Server) newLiveJobFilter(repGroup string, match RepGroupMatch, opts limitJobsOptions) *liveJobFilter {
	f := &liveJobFilter{s: s, opts: s.normalizeOptions(opts)}

	if match != RepGroupMatchExact {
		f.repGroup = repGroup
		f.match = match
	}

	if f.opts.Limit > 0 {
		f.kept = make(map[liveJobGroup]int)
		f.uncopied = make(map[liveJobGroup]int)
	}

	return f
}

// liveJobFilter decides, from a live job's own fields, whether limitJobs would
// keep the client copy of it, so that queries over the live queue copy only the
// jobs they return instead of every job in the queue.
//
// It applies the same RepGroup match as getJobsCurrent and the same filters as
// jobMatchesFilters. With a Limit it also does addJobToGroup's grouping: once a
// group holds Offset+Limit jobs, later members only add to its Similar count,
// so they are counted (see addUncopied) instead of copied.
type liveJobFilter struct {
	s        *Server
	repGroup string
	match    RepGroupMatch
	opts     limitJobsOptions
	kept     map[liveJobGroup]int
	uncopied map[liveJobGroup]int
}

// admits says whether the client copy of sjob, which must be read-locked, would
// have the given state and be kept by limitJobs. A nil filter admits every job.
func (f *liveJobFilter) admits(sjob *Job, state JobState) bool {
	if f == nil {
		return true
	}

	// the client copy does not carry Lost; its state already says lost
	state = f.s.normalizeJobState(state, false)

	if !f.matches(sjob, state) {
		return false
	}

	return f.keeps(liveJobGroup{state: state, exitCode: sjob.Exitcode, failReason: sjob.FailReason})
}

// matches says whether sjob, which must be read-locked, with the given
// normalised state passes the filter's RepGroup match and jobMatchesFilters.
func (f *liveJobFilter) matches(sjob *Job, state JobState) bool {
	if f.repGroup != "" && !RepGroupMatches(sjob.RepGroup, f.repGroup, f.match) {
		return false
	}

	if f.opts.WaitingForDepGroups && len(sjob.WaitingForDepGroups) == 0 {
		return false
	}

	return f.s.matchesStateFilter(state, f.opts.State) &&
		f.s.matchesFailureFilter(sjob.FailReason, sjob.Exitcode, f.opts.FailReason, f.opts.ExitCode)
}

// keeps says whether a matching job in the given group is one of the group's
// first Offset+Limit, counting it as uncopied if not. Without a Limit every
// matching job is kept.
func (f *liveJobFilter) keeps(group liveJobGroup) bool {
	if f.opts.Limit == 0 {
		return true
	}

	if f.kept[group] < f.opts.Offset+f.opts.Limit {
		f.kept[group]++

		return true
	}

	f.uncopied[group]++

	return false
}

// addUncopied adds the filter's counts of jobs it did not copy to counts, keyed
// by jobGroup as countUndecodedJobs wants them, returning the result, which is
// a new map if counts was nil.
func (f *liveJobFilter) addUncopied(counts map[string]int) map[string]int {
	if len(f.uncopied) == 0 {
		return counts
	}

	if counts == nil {
		counts = make(map[string]int, len(f.uncopied))
	}

	for g, n := range f.uncopied {
		counts[jobGroup(g.state, g.exitCode, g.failReason)] += n
	}

	return counts
}
