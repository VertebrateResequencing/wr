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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestExcessKillSparesElementReservedMidCycle covers DEVELOPERS.md rule 5 against
// the race seen in the 2026-09-25/26 limit-drain and report-storm-lsf sweeps: an
// LSF element that bjobs reports as PEND starts and claims a job reservation
// while killExcessCmds is still working from that bjobs snapshot. The element
// must not then be bkilled as excess.
//
// The fake bjobs blocks until the test has made the claim (exactly what the
// server does on a reserve from that element), so the interleaving is
// deterministic.
func TestExcessKillSparesElementReservedMidCycle(t *testing.T) {
	Convey("Given an excess PEND element that claims a reservation while bjobs is running", t, func() {
		dir := t.TempDir()
		started := filepath.Join(dir, "bjobs.started")
		proceed := filepath.Join(dir, "bjobs.proceed")

		const element = "4242[1]"

		bjobsExe := filepath.Join(dir, "bjobs")
		writeFakeExe(t, bjobsExe, "#!/bin/bash\ntouch "+started+"\n"+
			"for i in $(seq 1 200); do [ -e "+proceed+" ] && break; sleep 0.05; done\n"+
			"echo '4242 sb10 PEND normal host1 host2 wrd_fakecmd.uniq[1] Sep 26 08:00'\n")

		s, argvFile := newExcessKillLSF(t, dir, bjobsExe)

		claimed := make(chan bool, 1)

		go func() {
			if !waitForFile(started, 10*time.Second) {
				claimed <- false

				return
			}

			ok := s.claimForReserve(element)

			if err := os.WriteFile(proceed, nil, 0600); err != nil {
				ok = false
			}

			claimed <- ok
		}()

		count, err := s.killExcessCmds(context.Background(), bkillTestPrefix, 0)
		So(err, ShouldBeNil)
		So(<-claimed, ShouldBeTrue)

		Convey("the element holding the reservation is not bkilled, and still counts", func() {
			So(bkilledIDs(t, argvFile), ShouldNotContain, element)
			So(count, ShouldEqual, 1)
		})
	})
}

// TestExcessKillRefusesClaimFromDoomedElement covers the other side of the same
// race: once killExcessCmds has decided to bkill an element, a runner that
// starts in it before LSF kills it must be refused a reservation, rather than be
// handed a job and then killed.
func TestExcessKillRefusesClaimFromDoomedElement(t *testing.T) {
	Convey("Given one wanted and one excess PEND element", t, func() {
		dir := t.TempDir()

		const (
			wanted = "4242[1]"
			excess = "4242[2]"
		)

		bjobsExe := filepath.Join(dir, "bjobs")
		writeFakeExe(t, bjobsExe, "#!/bin/bash\n"+
			"echo '4242 sb10 PEND normal host1 host2 wrd_fakecmd.uniq[1] Sep 26 08:00'\n"+
			"echo '4242 sb10 PEND normal host1 host2 wrd_fakecmd.uniq[2] Sep 26 08:00'\n")

		s, argvFile := newExcessKillLSF(t, dir, bjobsExe)

		count, err := s.killExcessCmds(context.Background(), bkillTestPrefix, 1)
		So(err, ShouldBeNil)
		So(count, ShouldEqual, 1)
		So(bkilledIDs(t, argvFile), ShouldContain, excess)

		Convey("the excess element's claim is refused, while the wanted one's is allowed", func() {
			So(s.claimForReserve(excess), ShouldBeFalse)
			So(s.claimForReserve(wanted), ShouldBeTrue)
		})

		Convey("the doomed excess element is forgotten once bjobs stops reporting it", func() {
			writeFakeExe(t, bjobsExe, "#!/bin/bash\n"+
				"echo '4242 sb10 RUN normal host1 host2 wrd_fakecmd.uniq[1] Sep 26 08:00'\n")

			_, err = s.killExcessCmds(context.Background(), bkillTestPrefix, 1)
			So(err, ShouldBeNil)

			s.reservedMu.Lock()
			defer s.reservedMu.Unlock()

			So(s.doomedElements, ShouldBeEmpty)
		})
	})
}

// newExcessKillLSF returns an lsf scheduler that uses the given fake bjobs and a
// fake bkill that reports every id it is given as terminated and records its
// argv in the returned file.
func newExcessKillLSF(t *testing.T, dir, bjobsExe string) (*lsf, string) {
	t.Helper()

	argvFile := filepath.Join(dir, "bkill.argv")

	bkillExe := filepath.Join(dir, "bkill")
	writeFakeExe(t, bkillExe, "#!/bin/bash\nprintf '%s\\n' \"$*\" >> "+argvFile+"\n"+
		"for a in \"$@\"; do [ \"$a\" = -b ] || echo \"Job <$a> is being terminated\"; done\n")

	setBkillTunables(t, time.Minute, 10*time.Minute, 30*time.Minute)

	return &lsf{config: &ConfigLSF{Shell: testShell}, bjobsExe: bjobsExe, bkillExe: bkillExe}, argvFile
}

// waitForFile reports whether the given path exists within the given time.
func waitForFile(path string, limit time.Duration) bool {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}

	return false
}

// bkilledIDs returns every argument the fake bkill was called with.
func bkilledIDs(t *testing.T, argvFile string) []string {
	t.Helper()

	data, err := os.ReadFile(argvFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	return strings.Fields(string(data))
}
