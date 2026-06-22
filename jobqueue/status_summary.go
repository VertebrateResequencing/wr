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
	"math"
	"time"
)

// StatusMeasure stores mergeable running statistics for a numeric status
// summary value.
type StatusMeasure struct {
	N         uint
	MeanValue float64
	M2        float64
}

// Push adds one value to the running statistics.
func (m *StatusMeasure) Push(value float64) {
	m.N++
	if m.N == 1 {
		m.MeanValue = value

		return
	}

	delta := value - m.MeanValue
	m.MeanValue += delta / float64(m.N)
	m.M2 += delta * (value - m.MeanValue)
}

// Merge adds another measure's values into this one.
func (m *StatusMeasure) Merge(other StatusMeasure) {
	if other.N == 0 {
		return
	}

	if m.N == 0 {
		*m = other

		return
	}

	total := m.N + other.N
	delta := other.MeanValue - m.MeanValue
	m.M2 += other.M2 + delta*delta*float64(m.N)*float64(other.N)/float64(total)
	m.MeanValue += delta * float64(other.N) / float64(total)
	m.N = total
}

// NumDataValues returns how many values have been added.
func (m StatusMeasure) NumDataValues() uint {
	return m.N
}

// Mean returns the current mean.
func (m StatusMeasure) Mean() float64 {
	return m.MeanValue
}

// StandardDeviation returns the sample standard deviation.
func (m StatusMeasure) StandardDeviation() float64 {
	if m.N <= 1 {
		return 0
	}

	return math.Sqrt(m.M2 / (float64(m.N) - 1))
}

// RepGroupStatus is a compact status summary for one report group.
type RepGroupStatus struct {
	Counts    map[JobState]int
	Buried    map[string][]string
	Memory    StatusMeasure
	Disk      StatusMeasure
	Walltime  StatusMeasure
	CPUtime   StatusMeasure
	StartTime time.Time
	EndTime   time.Time
}

// NewRepGroupStatus returns an initialised report-group status summary.
func NewRepGroupStatus() *RepGroupStatus {
	return &RepGroupStatus{
		Counts: make(map[JobState]int),
		Buried: make(map[string][]string),
	}
}

// AddState adds count jobs to a state.
func (s *RepGroupStatus) AddState(state JobState, count int) {
	if count <= 0 {
		return
	}

	s.ensureMaps()

	if state == JobStateReserved {
		state = JobStateRunning
	}

	s.Counts[state] += count
}

// AddBuried records a buried job key by its exit-code/fail-reason group.
func (s *RepGroupStatus) AddBuried(group string, key string) {
	s.ensureMaps()
	s.Buried[group] = append(s.Buried[group], key)
}

// AddCompleteJob adds a completed job's compact status details.
func (s *RepGroupStatus) AddCompleteJob(job *Job) {
	s.AddState(JobStateComplete, 1)
	s.Memory.Push(float64(job.PeakRAM))
	s.Disk.Push(float64(job.PeakDisk))
	s.Walltime.Push(float64(job.WallTime()))
	s.CPUtime.Push(float64(job.CPUtime))
	s.addStartEnd(job.StartTime, job.EndTime)
}

// Merge folds another report-group status into this one.
func (s *RepGroupStatus) Merge(other *RepGroupStatus) {
	if other == nil {
		return
	}

	s.ensureMaps()

	for state, count := range other.Counts {
		s.AddState(state, count)
	}

	for group, keys := range other.Buried {
		s.Buried[group] = append(s.Buried[group], keys...)
	}

	s.Memory.Merge(other.Memory)
	s.Disk.Merge(other.Disk)
	s.Walltime.Merge(other.Walltime)
	s.CPUtime.Merge(other.CPUtime)
	s.addStartEnd(other.StartTime, other.EndTime)
}

func (s *RepGroupStatus) ensureMaps() {
	if s.Counts == nil {
		s.Counts = make(map[JobState]int)
	}

	if s.Buried == nil {
		s.Buried = make(map[string][]string)
	}
}

func (s *RepGroupStatus) addStartEnd(start time.Time, end time.Time) {
	if start.IsZero() || end.IsZero() {
		return
	}

	if s.StartTime.IsZero() || start.Before(s.StartTime) {
		s.StartTime = start
	}

	if s.EndTime.IsZero() || end.After(s.EndTime) {
		s.EndTime = end
	}
}
