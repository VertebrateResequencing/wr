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
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
)

// transitionChangedFields are the persisted Job fields outside jobRunState
// that a transition changes after the add without writing a new spec: release
// and bury (UntilBuried, FailBumpsCleared, ModifiedReqs, FailedRunReqs), kick
// and lost-job refresh (UntilBuried) and dependency resolution
// (WaitingForDepGroups). wr mod is a modify, which writes a new spec.
//
//nolint:gochecknoglobals // a read-only list.
var transitionChangedFields = []string{
	"UntilBuried", "FailBumpsCleared", "ModifiedReqs", "FailedRunReqs", "WaitingForDepGroups",
}

func TestFlatCodec(t *testing.T) {
	Convey("Given a bbolt-encoding db", t, func() {
		bdb := &db{ch: new(codec.BincHandle)}

		Convey("the fixture sets every field encodeJob persists", func() {
			So(zeroPersistedFields(fullFlatJob()), ShouldBeEmpty)
		})

		Convey("a Job with every persisted field set flat-decodes to what bbolt decodes", func() {
			job := fullFlatJob()

			So(flatRoundTrip(bdb, job), ShouldResemble, persistedCopy(bboltRoundTrip(bdb, job)))
		})

		Convey("cleanup behaviours are dropped with CwdMatters and kept without, as bbolt's decode does", func() {
			job := fullFlatJob()

			got := flatRoundTrip(bdb, job)

			So(got, ShouldResemble, persistedCopy(bboltRoundTrip(bdb, job)))
			So(got.Behaviours, ShouldHaveLength, 1)

			job.CwdMatters = false
			got = flatRoundTrip(bdb, job)

			So(got, ShouldResemble, persistedCopy(bboltRoundTrip(bdb, job)))
			So(got.Behaviours, ShouldHaveLength, 2)
		})

		Convey("a Job with times in UTC, Local and an odd offset round-trips as bbolt's does", func() {
			job := fullFlatJob()
			job.StartTime = time.Date(2026, 7, 1, 12, 0, 0, 5, time.Local) //nolint:gosmopolitan // the manager's zone
			job.EndTime = time.Date(2026, 7, 1, 13, 0, 0, 0, time.UTC)
			job.ReadyTime = time.Date(2026, 7, 1, 11, 0, 0, 1, time.FixedZone("x", -(5*3600+30*60+17)))

			So(flatRoundTrip(bdb, job), ShouldResemble, persistedCopy(bboltRoundTrip(bdb, job)))
		})

		Convey("a zero Job round-trips as bbolt's does", func() {
			job := &Job{}

			So(flatRoundTrip(bdb, job), ShouldResemble, persistedCopy(bboltRoundTrip(bdb, job)))
		})

		Convey("empty but non-nil slices and maps stay non-nil, as bbolt's do", func() {
			job := &Job{
				LimitGroups: []string{}, Modules: []string{}, StdOutC: []byte{}, EnvC: []byte{},
				Behaviours: Behaviours{}, Dependencies: Dependencies{}, MountConfigs: MountConfigs{},
				Requirements:        &jqs.Requirements{},
				RequirementsOrig:    &jqs.Requirements{},
				WaitingForDepGroups: []string{},
			}

			got := flatRoundTrip(bdb, job)

			So(got, ShouldResemble, persistedCopy(bboltRoundTrip(bdb, job)))
			So(got.LimitGroups, ShouldNotBeNil)
			So(got.WaitingForDepGroups, ShouldNotBeNil)
			So(got.Behaviours, ShouldNotBeNil)

			rs := logRunState{
				jobRunState:         jobRunState{StdOutC: []byte{}, Requirements: &jqs.Requirements{Other: map[string]string{}}},
				ModifiedReqs:        &jqs.Requirements{Other: map[string]string{}},
				WaitingForDepGroups: []string{},
			}

			var decoded logRunState
			So(decodeFlatRunState(appendFlatRunState(nil, &rs), &decoded), ShouldBeNil)
			So(decoded, ShouldResemble, bboltRunStateRoundTrip(bdb, rs))
			So(decoded.Requirements.Other, ShouldNotBeNil)
			So(decoded.ModifiedReqs.Other, ShouldNotBeNil)
		})

		Convey("a destination Job reused from another job takes only the new job's values", func() {
			first := fullFlatJob()
			second := &Job{
				Cmd: "other", Requirements: &jqs.Requirements{RAM: 1},
				LimitGroups: []string{"x"}, Modules: []string{"m2", "m3"}, DepGroups: []string{},
			}

			dst := &Job{}
			So(decodeFlatSpec(mustFlatSpec(bdb, first), dst, bdb.ch), ShouldBeNil)
			So(decodeFlatSpec(mustFlatSpec(bdb, second), dst, bdb.ch), ShouldBeNil)

			want := persistedCopy(bboltRoundTrip(bdb, second))
			dropRunState(want)

			So(persistedCopy(dst), ShouldResemble, want)
		})

		Convey("a Job with only one of Dependencies, Behaviours and MountConfigs set round-trips as bbolt's does", func() {
			for _, set := range []func(j *Job){
				func(j *Job) { j.Dependencies = fullFlatJob().Dependencies },
				func(j *Job) { j.Behaviours = fullFlatJob().Behaviours },
				func(j *Job) { j.MountConfigs = fullFlatJob().MountConfigs },
			} {
				job := &Job{Cmd: "one blob field"}
				set(job)

				So(flatRoundTrip(bdb, job), ShouldResemble, persistedCopy(bboltRoundTrip(bdb, job)))
			}
		})

		Convey("each flag bit decodes to its own field", func() {
			const flagsPerByte = 4 // the most bools any one flags byte holds

			for i := range flagsPerByte {
				set := func(bit int) bool { return bit == i }

				job := fullFlatJob()
				job.CwdMatters, job.ChangeHome = set(0), set(1)
				job.ContainerImageUser, job.EnvCRetrieved = set(2), set(3)
				job.Exited, job.Lost, job.RerunAfterRun = set(0), set(1), set(2)

				for _, req := range []*jqs.Requirements{job.Requirements, job.RequirementsOrig,
					job.ModifiedReqs, job.FailedRunReqs} {
					req.CoresSet, req.DiskSet, req.OtherSet = set(0), set(1), set(2)
				}

				So(flatRoundTrip(bdb, job), ShouldResemble, persistedCopy(bboltRoundTrip(bdb, job)))
			}
		})

		Convey("a run state snapshot does not change when its Job's Requirements or dep groups do", func() {
			job := fullFlatJob()
			rs := newLogRunState(job)
			want := fullFlatJob()

			job.ModifiedReqs.RAM++
			job.ModifiedReqs.Other["added"] = "1"
			job.FailedRunReqs.RAM++
			job.FailedRunReqs.Other["added"] = "1"
			job.WaitingForDepGroups[0] = "dg-renamed"

			So(rs.ModifiedReqs, ShouldResemble, want.ModifiedReqs)
			So(rs.FailedRunReqs, ShouldResemble, want.FailedRunReqs)
			So(rs.WaitingForDepGroups, ShouldResemble, want.WaitingForDepGroups)
		})

		Convey("a spec decoded after a run state leaves the run state's fields alone", func() {
			specFrom, stateFrom := &Job{}, fullFlatJob()
			rs := newLogRunState(stateFrom)

			var decoded logRunState
			So(decodeFlatRunState(appendFlatRunState(nil, &rs), &decoded), ShouldBeNil)

			got := &Job{}
			decoded.applyTo(got)
			So(decodeFlatSpec(mustFlatSpec(bdb, specFrom), got, bdb.ch), ShouldBeNil)

			So(fieldsNotFrom(got, bboltRoundTrip(bdb, specFrom), bboltRoundTrip(bdb, stateFrom)), ShouldBeEmpty)
		})

		Convey("appending to a decoded byte slice does not change the record", func() {
			spec := mustFlatSpec(bdb, fullFlatJob())
			rs := newLogRunState(fullFlatJob())
			state := appendFlatRunState(nil, &rs)
			specBefore, stateBefore := bytes.Clone(spec), bytes.Clone(state)

			job := &Job{}
			So(decodeFlatSpec(spec, job, bdb.ch), ShouldBeNil)

			var decoded logRunState
			So(decodeFlatRunState(state, &decoded), ShouldBeNil)

			_ = append(job.EnvC, "XXXXXXXX"...)
			_ = append(job.EnvOverride, "XXXXXXXX"...)
			_ = append(decoded.StdOutC, "XXXXXXXX"...)
			_ = append(decoded.StdErrC, "XXXXXXXX"...)

			So(spec, ShouldResemble, specBefore)
			So(state, ShouldResemble, stateBefore)
		})

		Convey("a spec whose blob is not Binc is malformed", func() {
			noBlob := fullFlatJob()
			noBlob.Dependencies, noBlob.Behaviours, noBlob.MountConfigs = nil, nil, nil
			spec := mustFlatSpec(bdb, noBlob)

			corrupt := appendFlatBytes(spec[:len(spec)-1], []byte{0xff, 0xff, 0xff})

			So(decodeFlatSpec(corrupt, &Job{}, bdb.ch), ShouldWrap, errFlatMalformed)
		})

		Convey("a spec whose blob holds a nil Behaviour or Dependency is malformed", func() {
			for _, job := range nilElementJobs() {
				err := decodeFlatSpec(mustFlatSpec(bdb, job), &Job{}, bdb.ch)

				So(err, ShouldWrap, errFlatMalformed)
				So(err, ShouldWrap, errNilStoredElement)
			}
		})

		Convey("a bbolt record holding a nil Behaviour or Dependency does not decode", func() {
			for _, job := range nilElementJobs() {
				encoded, err := bdb.encodeJob(job)
				So(err, ShouldBeNil)

				decoded, err := bdb.decodeJob(encoded)

				So(err, ShouldWrap, errNilStoredElement)
				So(decoded, ShouldBeNil)
			}
		})

		Convey("a run state decoded into again leaves the dep groups of a Job it was applied to alone", func() {
			a := logRunState{WaitingForDepGroups: []string{"first-dg"}}
			b := logRunState{WaitingForDepGroups: []string{"second-dg"}}

			var rs logRunState
			So(decodeFlatRunState(appendFlatRunState(nil, &a), &rs), ShouldBeNil)

			job := &Job{}
			rs.applyTo(job)

			So(decodeFlatRunState(appendFlatRunState(nil, &b), &rs), ShouldBeNil)
			So(job.WaitingForDepGroups, ShouldResemble, []string{"first-dg"})
		})

		Convey("a version byte of 0 is malformed", func() {
			rs := newLogRunState(fullFlatJob())
			spec, state := mustFlatSpec(bdb, fullFlatJob()), appendFlatRunState(nil, &rs)
			spec[0], state[0] = 0, 0

			So(decodeFlatSpec(spec, &Job{}, bdb.ch), ShouldWrap, errFlatMalformed)
			So(decodeFlatRunState(state, &logRunState{}), ShouldWrap, errFlatMalformed)
		})

		Convey("out-of-range numbers and tags in a run state are malformed", func() {
			base := newLogRunState(fullFlatJob())
			variant := func(change func(rs *logRunState)) []byte {
				rs := base
				change(&rs)

				return appendFlatRunState(nil, &rs)
			}

			cases := [][]byte{
				patchAtFirstDiff(
					variant(func(rs *logRunState) { rs.Attempts = 0 }),
					variant(func(rs *logRunState) { rs.Attempts = 1 }),
					binary.AppendUvarint(nil, math.MaxUint32+1)),
				patchAtFirstDiff(
					variant(func(rs *logRunState) { rs.StartTime = time.Unix(5, 0).UTC() }),
					variant(func(rs *logRunState) { rs.StartTime = time.Unix(5, 1).UTC() }),
					binary.AppendUvarint(nil, uint64(time.Second))),
				patchAtFirstDiff(
					variant(func(rs *logRunState) { rs.StartTime = time.Unix(5, 0).UTC() }),
					variant(func(rs *logRunState) { rs.StartTime = time.Unix(5, 1).UTC() }),
					binary.AppendUvarint(nil, math.MaxUint64)),
				patchAtFirstDiff(
					variant(func(rs *logRunState) { rs.EndTime = time.Unix(5, 0).UTC() }),
					variant(func(rs *logRunState) { rs.EndTime = time.Unix(5, 0).In(time.FixedZone("", 60)) }),
					binary.AppendVarint(nil, 1<<20)),
				patchAtFirstDiff(
					variant(func(rs *logRunState) {}),
					variant(func(rs *logRunState) { rs.Requirements = nil }),
					[]byte{flatReqsPresent + 1}),
			}

			for _, encoded := range cases {
				So(decodeFlatRunState(encoded, &logRunState{}), ShouldWrap, errFlatMalformed)
			}
		})

		Convey("a record claiming more entries than it has bytes is rejected without allocating for them", func() {
			huge := binary.AppendUvarint(nil, 1<<24)

			noBlob := &Job{LimitGroups: []string{"a"}}
			spec := patchAtFirstDiff(mustFlatSpec(bdb, noBlob), mustFlatSpec(bdb, &Job{LimitGroups: []string{}}), huge)

			withOther := logRunState{jobRunState: jobRunState{
				Requirements: &jqs.Requirements{Other: map[string]string{"k": "v"}},
			}}
			emptyOther := logRunState{jobRunState: jobRunState{
				Requirements: &jqs.Requirements{Other: map[string]string{}},
			}}
			state := patchAtFirstDiff(appendFlatRunState(nil, &withOther), appendFlatRunState(nil, &emptyOther), huge)

			var specErr, stateErr error

			allocated := bytesAllocated(func() {
				specErr = decodeFlatSpec(spec, &Job{}, bdb.ch)
				stateErr = decodeFlatRunState(state, &logRunState{})
			})

			So(specErr, ShouldWrap, errFlatMalformed)
			So(stateErr, ShouldWrap, errFlatMalformed)
			So(allocated, ShouldBeLessThan, 1<<20)
		})

		Convey("the run state carries exactly the jobRunState fields and the fields transitions change", func() {
			specFrom, stateFrom := &Job{}, fullFlatJob()
			rs := newLogRunState(stateFrom)

			got := &Job{}
			So(decodeFlatSpec(mustFlatSpec(bdb, specFrom), got, bdb.ch), ShouldBeNil)

			var decoded logRunState
			So(decodeFlatRunState(appendFlatRunState(nil, &rs), &decoded), ShouldBeNil)
			decoded.applyTo(got)

			So(fieldsNotFrom(got, bboltRoundTrip(bdb, specFrom), bboltRoundTrip(bdb, stateFrom)), ShouldBeEmpty)
		})

		Convey("a run state record round-trips every logRunState field", func() {
			rs := newLogRunState(fullFlatJob())

			var got logRunState
			So(decodeFlatRunState(appendFlatRunState(nil, &rs), &got), ShouldBeNil)
			So(got, ShouldResemble, bboltRunStateRoundTrip(bdb, rs))
		})

		Convey("a decoded run state does not share Requirements with the state decoded before it", func() {
			rs := newLogRunState(fullFlatJob())
			encoded := appendFlatRunState(nil, &rs)

			var reused logRunState
			So(decodeFlatRunState(encoded, &reused), ShouldBeNil)
			first := reused.Requirements

			So(decodeFlatRunState(encoded, &reused), ShouldBeNil)
			So(reused.Requirements, ShouldNotPointTo, first)
		})

		Convey("a spec whose version byte is 2 does not decode", func() {
			encoded := mustFlatSpec(bdb, fullFlatJob())
			encoded[0] = 2

			So(decodeFlatSpec(encoded, &Job{}, bdb.ch), ShouldWrap, errStoreVersionTooNew)
		})

		Convey("a run state whose version byte is 2 does not decode", func() {
			rs := newLogRunState(fullFlatJob())
			encoded := appendFlatRunState(nil, &rs)
			encoded[0] = 2

			So(decodeFlatRunState(encoded, &logRunState{}), ShouldWrap, errStoreVersionTooNew)
		})

		Convey("every truncation of a spec or run state is an error", func() {
			spec := mustFlatSpec(bdb, fullFlatJob())
			rs := newLogRunState(fullFlatJob())
			state := appendFlatRunState(nil, &rs)

			So(truncationsDecoded(spec, func(b []byte) error { return decodeFlatSpec(b, &Job{}, bdb.ch) }), ShouldEqual, 0)
			So(truncationsDecoded(state, func(b []byte) error { return decodeFlatRunState(b, &logRunState{}) }), ShouldEqual, 0)
			So(decodeFlatSpec(append(spec, 0), &Job{}, bdb.ch), ShouldWrap, errFlatMalformed)
			So(decodeFlatRunState(append(state, 0), &logRunState{}), ShouldWrap, errFlatMalformed)
		})
	})
}

// zeroPersistedFields names job's exported fields, the ones encodeJob
// persists, that are zero.
func zeroPersistedFields(job *Job) []string {
	v := reflect.ValueOf(job).Elem()

	var zero []string

	for i := range v.NumField() {
		field := v.Type().Field(i)
		if field.IsExported() && !field.Anonymous && v.Field(i).IsZero() {
			zero = append(zero, field.Name)
		}
	}

	return zero
}

func TestFlatCodecAllocations(t *testing.T) {
	Convey("Given a 10KiB-command Job with no blob fields", t, func() {
		ch := new(codec.BincHandle)
		job := fullFlatJob()
		job.Cmd = strings.Repeat("x", 10*1024)
		job.Dependencies, job.Behaviours, job.MountConfigs = nil, nil, nil

		spec, err := appendFlatSpec(nil, job, ch)
		So(err, ShouldBeNil)

		Convey("decoding its spec 1,000 times into a reused Job allocates nothing", func() {
			want := persistedCopy(bboltRoundTrip(&db{ch: ch}, job))
			dropRunState(want)

			dst := &Job{}

			var decodeErr error

			allocs := testing.AllocsPerRun(1000, func() {
				if err := decodeFlatSpec(spec, dst, ch); err != nil {
					decodeErr = err
				}
			})

			So(decodeErr, ShouldBeNil)
			So(persistedCopy(dst), ShouldResemble, want)
			So(allocs, ShouldEqual, 0)
		})

		Convey("encoding its spec into a big enough buffer allocates nothing", func() {
			buf := make([]byte, 0, 2*len(spec))

			var encodeErr error

			allocs := testing.AllocsPerRun(1000, func() {
				if _, err := appendFlatSpec(buf[:0], job, ch); err != nil {
					encodeErr = err
				}
			})

			So(encodeErr, ShouldBeNil)
			So(allocs, ShouldEqual, 0)
		})

		Convey("encoding its run state into a big enough buffer allocates nothing", func() {
			rs := newLogRunState(job)
			buf := make([]byte, 0, 4096)

			allocs := testing.AllocsPerRun(1000, func() {
				buf = appendFlatRunState(buf[:0], &rs)
			})

			So(allocs, ShouldEqual, 0)
		})

		Convey("decoding a run state without Requirements or dep groups allocates nothing", func() {
			rs := newLogRunState(job)
			rs.Requirements, rs.RequirementsOrig, rs.ModifiedReqs, rs.FailedRunReqs = nil, nil, nil, nil
			rs.WaitingForDepGroups = nil
			encoded := appendFlatRunState(nil, &rs)

			var (
				got       logRunState
				decodeErr error
			)

			allocs := testing.AllocsPerRun(1000, func() {
				if err := decodeFlatRunState(encoded, &got); err != nil {
					decodeErr = err
				}
			})

			So(decodeErr, ShouldBeNil)
			So(allocs, ShouldEqual, 0)
		})
	})
}

func FuzzFlatSpec(f *testing.F) {
	ch := new(codec.BincHandle)
	bdb := &db{ch: ch}

	full := mustFlatSpec(bdb, fullFlatJob())
	noBlob := fullFlatJob()
	noBlob.Dependencies, noBlob.Behaviours, noBlob.MountConfigs = nil, nil, nil

	tooNew := mustFlatSpec(bdb, noBlob)
	tooNew[0] = 2

	f.Add([]byte{})
	f.Add([]byte{flatVersion})
	f.Add(mustFlatSpec(bdb, &Job{}))
	f.Add(mustFlatSpec(bdb, noBlob))
	f.Add(full)
	f.Add(full[:len(full)/2])
	f.Add(tooNew)
	f.Add([]byte{flatVersion, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f})

	f.Fuzz(func(t *testing.T, src []byte) {
		job := &Job{}
		if err := decodeFlatSpec(src, job, ch); err != nil {
			return
		}

		again, err := appendFlatSpec(nil, job, ch)
		if err != nil {
			t.Fatalf("decoded spec does not re-encode: %v", err)
		}

		job2 := &Job{}
		if err := decodeFlatSpec(again, job2, ch); err != nil {
			t.Fatalf("re-encoded spec does not decode: %v", err)
		}

		if !bytes.Equal(canonicalBinc(persistedCopy(job)), canonicalBinc(persistedCopy(job2))) {
			t.Fatalf("spec changed over a re-encode: %+v != %+v", job, job2)
		}
	})
}

func FuzzFlatRunState(f *testing.F) {
	full := newLogRunState(fullFlatJob())
	empty := logRunState{}

	f.Add([]byte{})
	f.Add([]byte{flatVersion})
	f.Add(appendFlatRunState(nil, &empty))
	f.Add(appendFlatRunState(nil, &full))
	f.Add([]byte{flatVersion, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f})

	f.Fuzz(func(t *testing.T, src []byte) {
		var rs logRunState
		if err := decodeFlatRunState(src, &rs); err != nil {
			return
		}

		var rs2 logRunState
		if err := decodeFlatRunState(appendFlatRunState(nil, &rs), &rs2); err != nil {
			t.Fatalf("re-encoded run state does not decode: %v", err)
		}

		if !bytes.Equal(canonicalBinc(&rs), canonicalBinc(&rs2)) {
			t.Fatalf("run state changed over a re-encode: %+v != %+v", rs, rs2)
		}
	})
}

// flatRoundTrip returns the persisted fields of a fresh Job given job's flat
// spec and flat run state.
func flatRoundTrip(bdb *db, job *Job) *Job {
	spec, err := appendFlatSpec(nil, job, bdb.ch)
	So(err, ShouldBeNil)

	rs := newLogRunState(job)
	state := appendFlatRunState(nil, &rs)

	got := &Job{}
	So(decodeFlatSpec(spec, got, bdb.ch), ShouldBeNil)

	var decoded logRunState
	So(decodeFlatRunState(state, &decoded), ShouldBeNil)

	decoded.applyTo(got)

	return persistedCopy(got)
}

// bboltRunStateRoundTrip returns rs Binc-encoded and decoded, as bbolt's
// run-state records hold it.
func bboltRunStateRoundTrip(bdb *db, rs logRunState) logRunState {
	encoded, err := bdb.encode(&rs)
	So(err, ShouldBeNil)

	var out logRunState

	So(codec.NewDecoderBytes(encoded, bdb.ch).Decode(&out), ShouldBeNil)

	return out
}

// bboltRoundTrip returns job as bbolt stores and reads it back.
func bboltRoundTrip(bdb *db, job *Job) *Job {
	encoded, err := bdb.encodeJob(job)
	So(err, ShouldBeNil)

	decoded, err := bdb.decodeJob(encoded)
	So(err, ShouldBeNil)

	return decoded
}

// mustFlatSpec returns job's flat spec.
func mustFlatSpec(bdb *db, job *Job) []byte {
	spec, err := appendFlatSpec(nil, job, bdb.ch)
	if err != nil {
		panic(err)
	}

	return spec
}

// fullFlatJob returns a Job with every field encodeJob persists set to a
// non-zero value, nested ones included.
func fullFlatJob() *Job {
	bst := time.FixedZone("BST", 3600)
	reqs := func(ram int) *jqs.Requirements {
		return &jqs.Requirements{
			RAM: ram, Time: 3 * time.Hour, Cores: 1.5, Disk: 10,
			Other:    map[string]string{"cloud_flavor": "m1", "queue": "long"},
			CoresSet: true, DiskSet: true, OtherSet: true,
		}
	}

	return &Job{
		Cmd: "echo full", Cwd: "/tmp/cwd", CwdMatters: true, ChangeHome: true,
		RepGroup: "rg", ReqGroup: "reqg", Group: "sched",
		Requirements: reqs(100), RequirementsOrig: reqs(50),
		Override: 2, Priority: 3, Retries: 4, NoRetriesOverWalltime: time.Hour,
		LimitGroups: []string{"l1", "l2:3"}, LimitGroupsForDisplay: []string{"l1"},
		Modules: []string{"mod/1"}, DepGroups: []string{"dg1", "dg2"},
		Dependencies: Dependencies{
			NewEssenceDependency("dep cmd", "/dep/cwd"),
			NewDepGroupDependency("dgx"),
		},
		WaitingForDepGroups: []string{"dgx"},
		Behaviours: Behaviours{
			{When: OnSuccess, Do: Run, Arg: "touch done"},
			{When: OnExit, Do: CleanupAll},
		},
		MountConfigs: MountConfigs{{
			Mount: "mnt", CacheBase: "/cache", Retries: 3, Verbose: true,
			Targets: []MountTarget{{Profile: "p", Path: "bucket/path", CacheDir: "c", Cache: true, Write: true}},
		}},
		BsubMode: "bm", MonitorDocker: "md", WithDocker: "img", WithSingularity: "sif",
		ContainerMounts: "/a:/b", ContainerImageUser: true,
		ActualCwd: "/tmp/cwd/actual", PeakRAM: 99, PeakDisk: 7, Exited: true, Exitcode: 3,
		Lost: true, FailReason: "oom", Pid: 1234, RunnerPid: 1233,
		Host: "host1", HostID: "hid", HostIP: "10.0.0.1",
		StartTime: time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC),
		EndTime:   time.Date(2026, 6, 2, 3, 4, 5, 7, bst),
		CPUtime:   time.Minute, StdErrC: []byte("err"), StdOutC: []byte("out"),
		EnvC: []byte("env"), EnvCRetrieved: true, EnvOverride: []byte("override"),
		State: JobStateRunning, Attempts: 1 << 20, UntilBuried: 2, FailBumpsCleared: 1,
		ModifiedReqs: reqs(200), FailedRunReqs: reqs(300),
		ReservedBy:    uuid.Must(uuid.NewV4()),
		RerunAfterRun: true, RunnerReservation: 1 << 40, EnvKey: "envkey", Similar: 9,
		Queue: "q", BsubID: 1 << 40, DelayTime: time.Second,
		ReadyTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// canonicalBinc returns v Binc-encoded with maps sorted, so equal values give
// equal bytes even when a float is NaN.
func canonicalBinc(v any) []byte {
	h := new(codec.BincHandle)
	h.Canonical = true

	var out []byte
	if err := codec.NewEncoderBytes(&out, h).Encode(v); err != nil {
		panic(err)
	}

	return out
}

// persistedCopy returns a new Job holding only job's persisted fields.
func persistedCopy(job *Job) *Job {
	out := &Job{}
	setPersistedFields(out, job)

	return out
}

// dropRunState zeroes job's logRunState fields.
func dropRunState(job *Job) {
	var rs logRunState

	rs.applyTo(job)
}

// fieldsNotFrom names got's persisted fields that do not equal the same field
// of stateFrom, for a jobRunState field or one of transitionChangedFields, or
// of specFrom, for any other.
func fieldsNotFrom(got, specFrom, stateFrom *Job) []string {
	inState := make(map[string]bool)

	rsType := reflect.TypeFor[jobRunState]()
	for i := range rsType.NumField() {
		inState[rsType.Field(i).Name] = true
	}

	for _, name := range transitionChangedFields {
		inState[name] = true
	}

	g, sp, st := reflect.ValueOf(got).Elem(), reflect.ValueOf(specFrom).Elem(), reflect.ValueOf(stateFrom).Elem()

	var wrong []string

	for i := range g.NumField() {
		field := g.Type().Field(i)
		if !field.IsExported() || field.Anonymous {
			continue
		}

		want := sp.Field(i)
		if inState[field.Name] {
			want = st.Field(i)
		}

		if !reflect.DeepEqual(g.Field(i).Interface(), want.Interface()) {
			wrong = append(wrong, field.Name)
		}
	}

	return wrong
}

// nilElementJobs returns Jobs holding a nil Behaviour (with CwdMatters, so
// dropImpossibleCleanups reads it) and a nil Dependency, which wr never writes.
func nilElementJobs() []*Job {
	return []*Job{
		{Cmd: "echo stored nil behaviour", CwdMatters: true, Behaviours: Behaviours{nil}},
		{Cmd: "echo stored nil dependency", Dependencies: Dependencies{NewDepGroupDependency("g"), nil}},
	}
}

// patchAtFirstDiff returns a with the byte at which it first differs from b
// replaced by repl. a and b are encodings of records differing in one field,
// so the byte is where that field starts.
func patchAtFirstDiff(a, b, repl []byte) []byte {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}

	So(i, ShouldBeLessThan, len(a))

	return slices.Concat(a[:i], repl, a[i+1:])
}

// bytesAllocated returns how many bytes the heap allocated while fn ran.
func bytesAllocated(fn func()) uint64 {
	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)

	return after.TotalAlloc - before.TotalAlloc
}

// truncationsDecoded counts the proper prefixes of encoded that decode
// without error.
func truncationsDecoded(encoded []byte, decode func([]byte) error) int {
	n := 0

	for i := range len(encoded) {
		if decode(encoded[:i]) == nil {
			n++
		}
	}

	return n
}
