/*******************************************************************************
 * Copyright (c) 2021-2022, 2026 Genome Research Ltd.
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

// Job.Key() used to rebuild and hash the whole command on every call, which a
// production-shaped soak with 20KB commands measured at 11.6-15.6GB allocated per
// 20 minutes in jobKeyConcat. These tests pin that a repeated Key() costs no
// allocation, and that the memo it now keeps never serves a stale key, however an
// input of the key changes.

import (
	"strings"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
)

// keyMemoCmdSize is the size of the commands the soak that found the cost ran.
const keyMemoCmdSize = 20 * 1024

// keyMemoChanged is the new value the tests give a string input of a Key().
const keyMemoChanged = "changed"

// newKeyMemoJob returns a job with every input of its Key() set, and a 20KB Cmd.
func newKeyMemoJob() *Job {
	return &Job{
		Cmd:        strings.Repeat("c", keyMemoCmdSize),
		Cwd:        "/cwd",
		CwdMatters: true,
		MountConfigs: MountConfigs{
			{Mount: "m", Targets: []MountTarget{{Profile: "p", Path: "bucket/a"}}},
		},
		WithDocker:         "image",
		ContainerMounts:    "/a:/b",
		ContainerImageUser: true,
	}
}

// uncachedKey returns the key a brand new Job with j's key inputs has, which has
// never been asked for its key before.
func uncachedKey(j *Job) string {
	mcs := make(MountConfigs, len(j.MountConfigs))
	for i, mc := range j.MountConfigs {
		mc.Targets = append([]MountTarget(nil), mc.Targets...)
		mcs[i] = mc
	}

	fresh := &Job{
		Cmd:                strings.Clone(j.Cmd),
		Cwd:                j.Cwd,
		CwdMatters:         j.CwdMatters,
		MountConfigs:       mcs,
		WithDocker:         j.WithDocker,
		WithSingularity:    j.WithSingularity,
		ContainerMounts:    j.ContainerMounts,
		ContainerImageUser: j.ContainerImageUser,
	}

	return fresh.Key()
}

// keyMemoMutations changes each input of a Job's Key() in turn, both by
// assigning the field and by changing a MountConfigs slice in place.
func keyMemoMutations() map[string]func(j *Job) {
	return map[string]func(j *Job){
		"Cmd":                           func(j *Job) { j.Cmd += "x" },
		"Cmd in the same length":        func(j *Job) { j.Cmd = strings.Repeat("d", keyMemoCmdSize) },
		"Cwd":                           func(j *Job) { j.Cwd = "/changed" },
		"CwdMatters":                    func(j *Job) { j.CwdMatters = false },
		"MountConfigs":                  func(j *Job) { j.MountConfigs = nil },
		"MountConfigs appended to":      func(j *Job) { j.MountConfigs = append(j.MountConfigs, MountConfig{Mount: "n"}) },
		"a MountConfig's Mount":         func(j *Job) { j.MountConfigs[0].Mount = keyMemoChanged },
		"a MountTarget's Path":          func(j *Job) { j.MountConfigs[0].Targets[0].Path = "bucket/b" },
		"a MountTarget's Profile":       func(j *Job) { j.MountConfigs[0].Targets[0].Profile = "q" },
		"a MountConfig's Targets":       func(j *Job) { j.MountConfigs[0].Targets = nil },
		"WithDocker":                    func(j *Job) { j.WithDocker = keyMemoChanged },
		"WithSingularity":               func(j *Job) { j.WithDocker, j.WithSingularity = "", "image" },
		"ContainerMounts":               func(j *Job) { j.ContainerMounts = "/c:/d" },
		"ContainerImageUser":            func(j *Job) { j.ContainerImageUser = false },
		"Cmd, after a Key() in-between": func(j *Job) { j.Cmd = "a"; _ = j.Key(); j.Cmd = "b" },
	}
}

func TestJobKeyMemo(t *testing.T) {
	Convey("A Job that has been asked for its key", t, func() {
		for name, mutate := range keyMemoMutations() {
			j := newKeyMemoJob()
			before := j.Key()

			mutate(j)

			Convey("gives a new key after a change to its "+name, func() {
				So(j.Key(), ShouldNotEqual, before)
				So(j.Key(), ShouldEqual, uncachedKey(j))
			})
		}

		Convey("gives the new key after a JobModifier changes each input", func() {
			for _, modify := range keyMemoModifiers() {
				j := newKeyMemoJob()
				before := j.Key()

				jm := NewJobModifer()
				modify(jm)

				j.Lock()
				jm.applyTo(j)
				j.Unlock()

				So(j.Key(), ShouldNotEqual, before)
				So(j.Key(), ShouldEqual, uncachedKey(j))
			}
		})

		Convey("encodes exactly as it did before it was asked", func() {
			j := newKeyMemoJob()
			unasked := encodeKeyMemoJob(j)
			key := j.Key()

			So(encodeKeyMemoJob(j), ShouldResemble, unasked)

			Convey("and decodes to a job with the same key", func() {
				So(decodeKeyMemoJob(unasked, &Job{}).Key(), ShouldEqual, key)
			})

			Convey("and decoding over another job that was asked gives the decoded job's key", func() {
				other := &Job{Cmd: keyMemoChanged}
				_ = other.Key()

				So(decodeKeyMemoJob(unasked, other).Key(), ShouldEqual, key)
			})
		})

		Convey("gives the same key to many concurrent callers", func() {
			j := newKeyMemoJob()
			want := uncachedKey(j)
			mismatches := keyMemoConcurrentMismatches(j, want)

			So(mismatches, ShouldEqual, 0)
		})

		Convey("allocates nothing to give its key again", func() {
			j := newKeyMemoJob()
			key := j.Key()

			allocs := testing.AllocsPerRun(100, func() {
				if j.Key() != key {
					panic("key changed")
				}
			})

			So(allocs, ShouldEqual, 0)
		})
	})
}

// keyMemoModifiers sets, on a JobModifier, a change to each input of the key of
// a job made by newKeyMemoJob.
func keyMemoModifiers() []func(jm *JobModifier) {
	return []func(jm *JobModifier){
		func(jm *JobModifier) { jm.SetCmd(keyMemoChanged) },
		func(jm *JobModifier) { jm.SetCwd("/changed") },
		func(jm *JobModifier) { jm.SetCwdMatters(false) },
		func(jm *JobModifier) { jm.SetMountConfigs(MountConfigs{{Mount: "n"}}) },
		func(jm *JobModifier) { jm.SetWithDocker(keyMemoChanged) },
		func(jm *JobModifier) { jm.SetContainerMounts("/c:/d") },
		func(jm *JobModifier) { jm.SetContainerImageUser(false) },
	}
}

// encodeKeyMemoJob encodes j the way the manager's database does.
func encodeKeyMemoJob(j *Job) []byte {
	var encoded []byte

	So(codec.NewEncoderBytes(&encoded, new(codec.BincHandle)).Encode(j), ShouldBeNil)

	return encoded
}

// decodeKeyMemoJob decodes encoded over into, the way the manager's database
// does, and returns into.
func decodeKeyMemoJob(encoded []byte, into *Job) *Job {
	So(codec.NewDecoderBytes(encoded, new(codec.BincHandle)).Decode(into), ShouldBeNil)

	return into
}

// keyMemoConcurrentMismatches asks j for its key from many goroutines at once,
// returning how many answers were not want.
func keyMemoConcurrentMismatches(j *Job, want string) int {
	const (
		goroutines = 16
		calls      = 500
	)

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		mismatches int
	)

	for range goroutines {
		wg.Go(func() {
			for range calls {
				if j.Key() != want {
					mu.Lock()
					mismatches++
					mu.Unlock()
				}
			}
		})
	}

	wg.Wait()

	return mismatches
}

// BenchmarkJobKey measures asking a job with a 20KB Cmd for its key repeatedly,
// as the manager does about 7 times per job.
func BenchmarkJobKey(b *testing.B) {
	j := newKeyMemoJob()

	b.ReportAllocs()

	for b.Loop() {
		_ = j.Key()
	}
}
