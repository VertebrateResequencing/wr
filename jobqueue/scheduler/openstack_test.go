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

package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/kballard/go-shellquote"
	. "github.com/smartystreets/goconvey/convey"
)

var (
	errUnexpectedRunCmd             = errors.New("unexpected RunCmd")
	errUnexpectedUpload             = errors.New("upload should not happen when the executable is already present")
	errUnexpectedBackgroundExeCheck = errors.New("background executable check was not expected")
)

const openstackExeTestCmd = "echo hello"

func TestOpenstackSpawnReleasesReservedQuotaOnEarlySpawnError(t *testing.T) {
	Convey("OpenStack spawn releases reserved quota when spawn fails before using quota", t, func() {
		debugCounter = 0
		debugEffect = "failBeforeUsingQuota"

		defer func() {
			debugCounter = 0
			debugEffect = ""
		}()

		s := &opst{
			config: &ConfigOpenStack{
				ServerKeepTime: time.Minute,
			},
		}
		req := &Requirements{
			RAM:   1024,
			Time:  time.Minute,
			Cores: 2,
			Disk:  20,
			Other: map[string]string{},
		}
		flavor := &cloud.Flavor{
			ID:    "tiny",
			Name:  "tiny",
			Cores: 2,
			RAM:   1024,
			Disk:  10,
		}

		s.spawn(context.Background(), req, flavor, "missing-os", nil, "", false, "true")

		s.resourceMutex.RLock()
		defer s.resourceMutex.RUnlock()

		So(s.reservedInstances, ShouldEqual, 0)
		So(s.reservedCores, ShouldEqual, 0)
		So(s.reservedRAM, ShouldEqual, 0)
		So(s.reservedVolume, ShouldEqual, 0)
	})
}

type fakeOpenstackExeServer struct {
	runCmds   []string
	uploads   int
	runCmd    func(cmd string, background bool) (string, string, error)
	uploadErr error
}

func (s *fakeOpenstackExeServer) RunCmd(
	_ context.Context, cmd string, background bool,
) (stdout, stderr string, err error) {
	s.runCmds = append(s.runCmds, cmd)

	if s.runCmd == nil {
		return "", "", errUnexpectedRunCmd
	}

	return s.runCmd(cmd, background)
}

func (s *fakeOpenstackExeServer) UploadFile(_ context.Context, _, _ string) error {
	s.uploads++

	return s.uploadErr
}

func TestOpenstackEnsureExeOnServer(t *testing.T) {
	Convey("OpenStack executable checks do not upload an executable that is already present remotely", t, func() {
		ctx := context.Background()
		cmd := "/bin/echo hello"
		s := newOpenstackExeTestScheduler(ctx, cmd)
		server := &fakeOpenstackExeServer{
			uploadErr: errUnexpectedUpload,
		}
		server.runCmd = func(_ string, background bool) (string, string, error) {
			if background {
				return "", "", errUnexpectedBackgroundExeCheck
			}

			return remoteExePresent, "", nil
		}

		err := s.ensureExeOnRemoteServer(ctx, "server-1", server, cmd)

		So(err, ShouldBeNil)
		So(server.runCmds, ShouldHaveLength, 1)
		So(server.runCmds[0], ShouldNotContainSubstring, "file ")
		So(server.runCmds[0], ShouldNotContainSubstring, "command -v")
		So(server.runCmds[0], ShouldContainSubstring, "test -x")
		So(server.uploads, ShouldEqual, 0)
	})

	Convey("OpenStack executable checks use remote PATH before the local absolute path", t, func() {
		ctx := context.Background()
		cmd := openstackExeTestCmd
		s := newOpenstackExeTestScheduler(ctx, cmd)
		server := &fakeOpenstackExeServer{
			uploadErr: errUnexpectedUpload,
		}
		server.runCmd = func(cmd string, background bool) (string, string, error) {
			if background {
				return "", "", errUnexpectedBackgroundExeCheck
			}

			if strings.Contains(cmd, "command -v") {
				return remoteExePresent, "", nil
			}

			return remoteExeMissing, "", nil
		}

		err := s.ensureExeOnRemoteServer(ctx, "server-1", server, cmd)

		So(err, ShouldBeNil)
		So(server.runCmds, ShouldHaveLength, 1)
		So(server.runCmds[0], ShouldContainSubstring, "command -v")
		So(server.uploads, ShouldEqual, 0)
	})

	Convey("OpenStack executable checks preserve upload behaviour when the executable is missing remotely", t, func() {
		ctx := context.Background()
		cmd := openstackExeTestCmd
		s := newOpenstackExeTestScheduler(ctx, cmd)
		server := new(fakeOpenstackExeServer)
		server.runCmd = func(cmd string, background bool) (string, string, error) {
			if background {
				return "", "", errUnexpectedBackgroundExeCheck
			}

			if len(server.runCmds) <= 2 {
				return remoteExeMissing, "", nil
			}

			return "", "", nil
		}

		err := s.ensureExeOnRemoteServer(ctx, "server-1", server, cmd)

		So(err, ShouldBeNil)
		So(server.uploads, ShouldEqual, 1)
		So(server.runCmds, ShouldHaveLength, 3)
		So(server.runCmds[0], ShouldContainSubstring, "command -v")
		So(server.runCmds[1], ShouldContainSubstring, "test -x")
		So(server.runCmds[2], ShouldStartWith, "chmod u+x ")
	})

	Convey("OpenStack executable uploads quote chmod paths", t, func() {
		ctx := context.Background()
		cmd := openstackExeTestCmd
		s := newOpenstackExeTestScheduler(ctx, cmd)
		server := new(fakeOpenstackExeServer)
		server.runCmd = func(_ string, background bool) (string, string, error) {
			if background {
				return "", "", errUnexpectedBackgroundExeCheck
			}

			return "", "", nil
		}

		exePath := "/tmp/wr exe; echo injected"
		err := s.uploadExe(ctx, "server-1", server, cmd, exePath)

		So(err, ShouldBeNil)
		So(server.uploads, ShouldEqual, 1)
		So(server.runCmds, ShouldResemble, []string{shellquote.Join("chmod", "u+x", exePath)})
	})

	Convey("OpenStack executable checks parse quoted command executables", t, func() {
		ctx := context.Background()
		cmd := `   "/bin/echo" hello`
		s := newOpenstackExeTestScheduler(ctx, cmd)
		server := &fakeOpenstackExeServer{
			uploadErr: errUnexpectedUpload,
		}
		server.runCmd = func(_ string, background bool) (string, string, error) {
			if background {
				return "", "", errUnexpectedBackgroundExeCheck
			}

			return remoteExePresent, "", nil
		}

		err := s.ensureExeOnRemoteServer(ctx, "server-1", server, cmd)

		So(err, ShouldBeNil)
		So(server.runCmds, ShouldHaveLength, 1)
		So(server.runCmds[0], ShouldContainSubstring, "test -x /bin/echo")
		So(server.uploads, ShouldEqual, 0)
	})

	Convey("OpenStack executable checks reject commands without an executable", t, func() {
		ctx := context.Background()
		cmd := "   "
		s := newOpenstackExeTestScheduler(ctx, cmd)
		server := new(fakeOpenstackExeServer)

		err := s.ensureExeOnRemoteServer(ctx, "server-1", server, cmd)

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "could not parse command executable")
		So(server.runCmds, ShouldHaveLength, 0)
		So(server.uploads, ShouldEqual, 0)
	})

	Convey("OpenStack executable checks reject malformed commands", t, func() {
		ctx := context.Background()
		cmd := `"unterminated`
		s := newOpenstackExeTestScheduler(ctx, cmd)
		server := new(fakeOpenstackExeServer)

		err := s.ensureExeOnRemoteServer(ctx, "server-1", server, cmd)

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "could not parse command executable")
		So(server.runCmds, ShouldHaveLength, 0)
		So(server.uploads, ShouldEqual, 0)
	})

	Convey("OpenStack executable lookup errors name the requested executable", t, func() {
		ctx := context.Background()
		cmd := "/tmp/wr-test-missing-exe"
		s := newOpenstackExeTestScheduler(ctx, cmd)
		server := new(fakeOpenstackExeServer)

		err := s.ensureExeOnRemoteServer(ctx, "server-1", server, cmd)

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "could not look for exe [/tmp/wr-test-missing-exe]")
		So(server.runCmds, ShouldHaveLength, 0)
		So(server.uploads, ShouldEqual, 0)
	})
}

func newOpenstackExeTestScheduler(ctx context.Context, cmd string) *opst {
	s := &opst{
		local: local{
			queue:   queue.New(ctx, localPlace),
			running: make(map[string]int),
		},
		spawnCanceller: make(map[string]map[string]chan struct{}),
	}

	_, err := s.queue.AddWithSize(ctx, jobName(cmd, "n/a", false), "", &job{cmd: cmd, count: 1}, 0, 0, 0, queueItemTTR, "")
	So(err, ShouldBeNil)

	return s
}
