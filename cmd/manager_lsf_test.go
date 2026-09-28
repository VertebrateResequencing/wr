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
	"context"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	testLSFQueueFlag       = "lsf_queue"
	testLSFQueuesAvoidFlag = "lsf_queues_avoid"
	testLSFQueueIgnored    = "to the lsf scheduler, so will be ignored"
)

func TestManagerStartLSFQueueFlags(t *testing.T) {
	flags := managerStartCmd.Flags()

	Convey("manager start's LSF queue flags default to the lsfqueue and lsfqueuesavoid config", t, func() {
		defaultConfig := internal.DefaultConfig(context.Background())

		queueFlag := flags.Lookup(testLSFQueueFlag)
		So(queueFlag, ShouldNotBeNil)
		So(queueFlag.DefValue, ShouldEqual, defaultConfig.LSFQueue)
		So(queueFlag.Usage, ShouldContainSubstring, "for the lsf scheduler")

		avoidFlag := flags.Lookup(testLSFQueuesAvoidFlag)
		So(avoidFlag, ShouldNotBeNil)
		So(avoidFlag.DefValue, ShouldEqual, defaultConfig.LSFQueuesAvoid)
		So(avoidFlag.Usage, ShouldContainSubstring, "replaces")
	})

	Convey("Given manager start's LSF queue flags on the command line", t, func() {
		oldScheduler, oldConfig := scheduler, config
		oldQueue, oldAvoid := managerLSFQueue, managerLSFQueuesAvoid

		defer func() {
			scheduler, config = oldScheduler, oldConfig
			managerLSFQueue, managerLSFQueuesAvoid = oldQueue, oldAvoid
			flags.Lookup(testLSFQueueFlag).Changed = false
			flags.Lookup(testLSFQueuesAvoidFlag).Changed = false
		}()

		config = &internal.Config{Deployment: internal.Development}

		err := flags.Parse([]string{"--" + testLSFQueueFlag, "normal,long", "--" + testLSFQueuesAvoidFlag, "yesterday,"})
		So(err, ShouldBeNil)

		Convey("they reach the LSF scheduler config", func() {
			scheduler = schedulerLSF

			schedulerConfig, _ := buildSchedulerConfig("wr", nil, nil)
			lsfConfig, ok := schedulerConfig.(*jqs.ConfigLSF)
			So(ok, ShouldBeTrue)
			So(lsfConfig.Queue, ShouldEqual, "normal,long")
			So(lsfConfig.QueuesAvoid, ShouldEqual, "yesterday,")
			So(lsfConfig.Deployment, ShouldEqual, internal.Development)
		})

		Convey("they are not warned about with the lsf scheduler", func() {
			logged := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			warnIgnoredLSFFlags(managerStartCmd, schedulerLSF)
			So(logged.String(), ShouldNotContainSubstring, testLSFQueueIgnored)
		})

		Convey("they are warned about as ignored with another scheduler", func() {
			logged := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			warnIgnoredLSFFlags(managerStartCmd, "local")
			So(logged.String(), ShouldContainSubstring, "--"+testLSFQueueFlag)
			So(logged.String(), ShouldContainSubstring, "--"+testLSFQueuesAvoidFlag)
			So(logged.String(), ShouldContainSubstring, testLSFQueueIgnored)
		})
	})

	Convey("A single LSF queue flag given with another scheduler is warned about on its own", t, func() {
		defer func() { flags.Lookup(testLSFQueueFlag).Changed = false }()

		oldQueue := managerLSFQueue
		defer func() { managerLSFQueue = oldQueue }()

		So(flags.Set(testLSFQueueFlag, "normal"), ShouldBeNil)

		logged := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		warnIgnoredLSFFlags(managerStartCmd, "local")
		So(logged.String(), ShouldContainSubstring, "--"+testLSFQueueFlag+" only applies to the lsf scheduler")
	})

	Convey("Unset LSF queue flags are not warned about with another scheduler", t, func() {
		logged := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		warnIgnoredLSFFlags(managerStartCmd, "openstack")
		So(logged.String(), ShouldNotContainSubstring, testLSFQueueIgnored)
	})
}
