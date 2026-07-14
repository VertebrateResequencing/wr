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
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	"go.nanomsg.org/mangos/v3"
)

const modifierValidationRepGroup = "modifier-validation"

type modifierJobSnapshot struct {
	key          string
	cmd          string
	cwd          string
	state        JobState
	queueState   string
	priority     uint8
	ram          int
	time         time.Duration
	cores        float64
	disk         int
	other        string
	depGroups    []string
	dependencies []string
	behaviours   []string
}

func snapshotModifierJob(job *Job, queueState string) modifierJobSnapshot {
	job.RLock()
	defer job.RUnlock()

	return modifierJobSnapshot{
		key: job.Key(), cmd: job.Cmd, cwd: job.Cwd, state: job.State, queueState: queueState,
		priority: job.Priority, ram: job.Requirements.RAM, time: job.Requirements.Time,
		cores: job.Requirements.Cores, disk: job.Requirements.Disk,
		other: fmt.Sprint(job.Requirements.Other), depGroups: slices.Clone(job.DepGroups),
		dependencies: snapshotDependencies(job.Dependencies),
		behaviours:   snapshotBehaviours(job.Behaviours),
	}
}

type modifierQueueSnapshot struct {
	keys  []string
	stats queue.Stats
	jobs  []modifierJobSnapshot
}

type modifierServerMode struct {
	mode          string
	drain         bool
	pauseRequests int
}

func TestServerRejectsModifyWithNilNestedEntriesAtomically(t *testing.T) {
	if runnermode || servermode {
		return
	}

	tests := []struct {
		name     string
		path     string
		modifier func() *JobModifier
	}{
		{
			name: "nil behaviour",
			path: "modifier.Behaviours[1] is nil",
			modifier: func() *JobModifier {
				modifier := atomicMalformedModifier()
				modifier.SetBehaviours(Behaviours{
					&Behaviour{When: OnFailure, Do: Remove}, nil,
				})

				return modifier
			},
		},
		{
			name: "nil dependency",
			path: "modifier.Dependencies[1] is nil",
			modifier: func() *JobModifier {
				modifier := atomicMalformedModifier()
				modifier.SetDependencies(Dependencies{
					NewDepGroupDependency("new-dependency"), nil,
				})

				return modifier
			},
		},
	}

	for _, test := range tests {
		Convey("An encoded modification containing a "+test.name+
			" is rejected before queue or persistence changes", t, func() {
			harness := newModifierValidationHarness(t)
			defer harness.close()

			beforeQueue := harness.queueSnapshot()
			beforeDB := harness.dbSnapshot()
			panicValue, err := harness.rawModify(test.modifier())

			var jqErr Error

			So(panicValue, ShouldBeNil)
			So(errors.As(err, &jqErr), ShouldBeTrue)
			So(jqErr.Op, ShouldEqual, "jmod")
			So(jqErr.Err, ShouldEqual, test.path)
			So(harness.sock.response().Err, ShouldEqual, ErrBadRequest)
			So(harness.queueSnapshot(), ShouldResemble, beforeQueue)
			So(harness.dbSnapshot(), ShouldResemble, beforeDB)
			So(harness.serverMode(), ShouldResemble, modifierServerMode{
				mode: ServerModeNormal,
			})

			harness.assertSubsequentValidModifications()
		})
	}
}

func atomicMalformedModifier() *JobModifier {
	modifier := NewJobModifer()
	modifier.SetCwd("/modified")
	modifier.SetPriority(9)
	modifier.SetDepGroups([]string{"new-index"})
	modifier.SetRequirements(&scheduler.Requirements{
		RAM: 999, Time: 99 * time.Second, Cores: 9, CoresSet: true,
		Disk: 9, DiskSet: true, Other: map[string]string{"changed": "yes"}, OtherSet: true,
	})

	return modifier
}

func newModifierValidationHarness(t *testing.T) *modifierValidationHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	tmpDir := t.TempDir()
	testDB, _, err := initDB(ctx, filepath.Join(tmpDir, "queue.db"),
		filepath.Join(tmpDir, "queue.db.bak"), internal.Development, false, false)
	So(err, ShouldBeNil)

	ch := new(codec.BincHandle)
	token := bytes.Repeat([]byte("x"), tokenLength)
	sock := &captureSocket{ch: ch}
	server := &Server{
		ch: ch, sock: sock, token: token, db: testDB,
		q: queue.New(ctx, "modifier-validation"), rpl: newRGToKeys(), up: true,
		ServerInfo: &ServerInfo{Mode: ServerModeNormal},
	}
	server.SetItemTTR(time.Minute)
	sock.server = server

	clientID, err := uuid.NewV4()
	So(err, ShouldBeNil)

	client := &Client{ch: ch, clientid: clientID, sock: sock, token: token}
	harness := &modifierValidationHarness{
		cancel: cancel, db: testDB, server: server, sock: sock, client: client,
	}
	harness.jobs = validationJobs()
	added, existed, err := client.Add(harness.jobs, []string{"MODIFIER_VALIDATION=1"}, false)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, len(harness.jobs))
	So(existed, ShouldEqual, 0)
	So(sock.serverErr, ShouldBeNil)

	return harness
}

func validationJobs() []*Job {
	jobs := make([]*Job, 0, 2)
	for i := range 2 {
		jobs = append(jobs, &Job{
			Cmd: fmt.Sprintf("echo original %d", i), Cwd: "/original", CwdMatters: true,
			RepGroup: modifierValidationRepGroup, ReqGroup: modifierValidationRepGroup,
			Requirements: &scheduler.Requirements{
				RAM: 100 + i, Time: 10 * time.Second, Cores: 1, Disk: 1,
				Other: map[string]string{"original": strconv.Itoa(i)},
			},
			Priority: 2, DepGroups: []string{"original-index"},
			Dependencies: Dependencies{NewDepGroupDependency("original-dependency")},
			Behaviours: Behaviours{
				&Behaviour{When: OnExit, Do: Nothing},
			},
		})
	}

	return jobs
}

type modifierValidationHarness struct {
	cancel context.CancelFunc
	db     *db
	server *Server
	sock   *captureSocket
	client *Client
	jobs   []*Job
}

func (h *modifierValidationHarness) close() {
	h.cancel()
	So(h.db.close(context.Background()), ShouldBeNil)
}

func (h *modifierValidationHarness) rawModify(modifier *JobModifier) (panicValue any, err error) {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, h.server.ch)
	if encodeErr := enc.Encode(&clientRequest{
		Method: "jmod", Token: h.server.token, Keys: h.originalKeys(), Modifier: modifier,
	}); encodeErr != nil {
		return nil, encodeErr
	}

	defer func() {
		panicValue = recover()
	}()

	err = h.server.handleRequest(context.Background(), &mangos.Message{Body: encoded})

	return panicValue, err
}

func (h *modifierValidationHarness) originalKeys() []string {
	keys := make([]string, 0, len(h.jobs))
	for _, job := range h.jobs {
		keys = append(keys, job.Key())
	}

	return keys
}

func (h *modifierValidationHarness) queueSnapshot() modifierQueueSnapshot {
	keys := h.server.rpl.Values(modifierValidationRepGroup)
	slices.Sort(keys)

	stats := *h.server.q.Stats()
	snapshot := modifierQueueSnapshot{keys: keys, stats: stats}

	for _, key := range keys {
		item, err := h.server.q.Get(key)
		if err != nil {
			snapshot.jobs = append(snapshot.jobs, modifierJobSnapshot{key: key, queueState: err.Error()})

			continue
		}

		job, ok := item.Data().(*Job)
		if !ok {
			snapshot.jobs = append(snapshot.jobs, modifierJobSnapshot{key: key, queueState: "not a Job"})

			continue
		}

		snapshot.jobs = append(snapshot.jobs, snapshotModifierJob(job, fmt.Sprint(item.Stats().State)))
	}

	return snapshot
}

func (h *modifierValidationHarness) dbSnapshot() map[string]map[string]string {
	buckets := [][]byte{
		bucketJobsLive, bucketRTK, bucketRGs, bucketDTK, bucketDepGroups,
		bucketRDTK, bucketJobLookupEntries, bucketEnvs, bucketLGs,
	}
	snapshot := make(map[string]map[string]string, len(buckets))

	err := h.db.bolt.View(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			values := make(map[string]string)

			bucket := tx.Bucket(name)
			if bucket == nil {
				continue
			}

			if err := bucket.ForEach(func(key, value []byte) error {
				values[string(key)] = string(value)

				return nil
			}); err != nil {
				return err
			}

			snapshot[string(name)] = values
		}

		return nil
	})
	So(err, ShouldBeNil)

	return snapshot
}

func (h *modifierValidationHarness) serverMode() modifierServerMode {
	h.server.ssmutex.RLock()
	defer h.server.ssmutex.RUnlock()

	return modifierServerMode{
		mode: h.server.ServerInfo.Mode, drain: h.server.drain, pauseRequests: h.server.pauseRequests,
	}
}

func (h *modifierValidationHarness) assertSubsequentValidModifications() {
	modifier := &JobModifier{
		Behaviours: Behaviours{nil}, Dependencies: Dependencies{nil},
	}
	modifier.SetPriority(7)
	modified, err := h.client.Modify(jobsToJobEssenses(h.jobs), modifier)

	So(err, ShouldBeNil)
	So(modified, ShouldHaveLength, len(h.jobs))

	modifier = modifierWithCollections(nil, nil)
	modified, err = h.client.Modify(jobsToJobEssenses(h.jobs), modifier)
	So(err, ShouldBeNil)
	So(modified, ShouldHaveLength, len(h.jobs))

	modifier = modifierWithCollections(
		Behaviours{&Behaviour{}}, Dependencies{&Dependency{}},
	)
	modified, err = h.client.Modify(jobsToJobEssenses(h.jobs), modifier)
	So(err, ShouldBeNil)
	So(modified, ShouldHaveLength, len(h.jobs))

	queued, err := h.client.GetByRepGroup(modifierValidationRepGroup, false, 0, "", false, false)
	So(err, ShouldBeNil)
	So(queued, ShouldHaveLength, len(h.jobs))

	sort.Slice(queued, func(i, j int) bool { return queued[i].Cmd < queued[j].Cmd })

	for _, job := range queued {
		So(job.Priority, ShouldEqual, 7)
		So(snapshotDependencies(job.Dependencies), ShouldResemble, []string{"<zero>"})
		So(snapshotBehaviours(job.Behaviours), ShouldResemble, []string{"1:16:<nil>", "0:0:<nil>"})
		So(job.RemovalRequested(), ShouldBeFalse)
		So(job.TriggerBehaviours(true), ShouldBeNil)
	}

	persisted, err := h.db.recoverIncompleteJobs()
	So(err, ShouldBeNil)
	So(persisted, ShouldHaveLength, len(h.jobs))

	for _, job := range persisted {
		So(job.Priority, ShouldEqual, 7)
		So(snapshotDependencies(job.Dependencies), ShouldResemble, []string{"<zero>"})
		So(snapshotBehaviours(job.Behaviours), ShouldResemble, []string{"1:16:<nil>", "0:0:<nil>"})
	}
}

func modifierWithCollections(behaviours Behaviours, dependencies Dependencies) *JobModifier {
	modifier := NewJobModifer()
	modifier.SetBehaviours(behaviours)
	modifier.SetDependencies(dependencies)

	return modifier
}

func snapshotDependencies(dependencies Dependencies) []string {
	values := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		switch {
		case dependency == nil:
			values = append(values, "<nil>")
		case dependency.DepGroup != "":
			values = append(values, "group:"+dependency.DepGroup)
		case dependency.Essence != nil:
			values = append(values, "essence:"+dependency.Essence.Stringify())
		default:
			values = append(values, "<zero>")
		}
	}

	return values
}

func snapshotBehaviours(behaviours Behaviours) []string {
	values := make([]string, 0, len(behaviours))
	for _, behaviour := range behaviours {
		if behaviour == nil {
			values = append(values, "<nil>")

			continue
		}

		values = append(values, fmt.Sprintf("%d:%d:%v", behaviour.When, behaviour.Do, behaviour.Arg))
	}

	return values
}

func TestClientModifyRejectsNilNestedModifierEntries(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Client Modify rejects malformed modifiers before sending them", t, func() {
		tests := []struct {
			name     string
			path     string
			modifier *JobModifier
		}{
			{
				name: "nil behaviour",
				path: "modifier.Behaviours[1] is nil",
				modifier: func() *JobModifier {
					modifier := NewJobModifer()
					modifier.SetBehaviours(Behaviours{&Behaviour{}, nil})

					return modifier
				}(),
			},
			{
				name: "nil dependency",
				path: "modifier.Dependencies[1] is nil",
				modifier: func() *JobModifier {
					modifier := NewJobModifer()
					modifier.SetDependencies(Dependencies{&Dependency{}, nil})

					return modifier
				}(),
			},
			{name: "nil modifier", path: "modifier is nil"},
		}

		for _, test := range tests {
			Convey(test.name, func() {
				client, sock := newCaptureClient()
				modified, err := client.Modify([]*JobEssence{{Cmd: "echo untouched"}}, test.modifier)

				var jqErr Error

				So(modified, ShouldBeNil)
				So(errors.As(err, &jqErr), ShouldBeTrue)
				So(jqErr, ShouldResemble, Error{
					Op: "jmod", Item: test.path, Err: ErrBadRequest,
				})
				So(sock.sent, ShouldBeNil)
			})
		}
	})

	Convey("Client Modify ignores nil entries in modifier fields that were not set", t, func() {
		client, sock := newCaptureClient()
		modifier := &JobModifier{
			Behaviours:   Behaviours{nil},
			Dependencies: Dependencies{nil},
		}
		modifier.SetPriority(7)

		modified, err := client.Modify([]*JobEssence{{Cmd: "echo valid"}}, modifier)

		So(err, ShouldBeNil)
		So(modified, ShouldHaveLength, 0)
		So(sock.request().Modifier.Priority, ShouldEqual, 7)
	})

	Convey("Client Modify accepts nil, empty, and zero-valued set collections", t, func() {
		validModifiers := []*JobModifier{
			modifierWithCollections(nil, nil),
			modifierWithCollections(Behaviours{}, Dependencies{}),
			modifierWithCollections(Behaviours{&Behaviour{}}, Dependencies{&Dependency{}}),
		}

		for _, modifier := range validModifiers {
			client, sock := newCaptureClient()
			modified, err := client.Modify([]*JobEssence{{Cmd: "echo valid collections"}}, modifier)

			So(err, ShouldBeNil)
			So(modified, ShouldHaveLength, 0)
			So(sock.sent, ShouldNotBeNil)
		}
	})
}
