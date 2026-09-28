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
	testQueueFlag       = flagQueue
	testQueuesAvoidFlag = flagQueuesAvoid
	testQueueIgnored    = "to schedulers with queues (currently only lsf), so will be ignored"
)

func TestManagerStartQueueFlags(t *testing.T) {
	flags := managerStartCmd.Flags()

	Convey("manager start's queue flags default to the managerqueue and managerqueuesavoid config", t, func() {
		defaultConfig := internal.DefaultConfig(context.Background())

		queueFlag := flags.Lookup(testQueueFlag)
		So(queueFlag, ShouldNotBeNil)
		So(queueFlag.DefValue, ShouldEqual, defaultConfig.ManagerQueue)
		So(queueFlag.Usage, ShouldContainSubstring, "for schedulers with queues (currently only lsf)")

		avoidFlag := flags.Lookup(testQueuesAvoidFlag)
		So(avoidFlag, ShouldNotBeNil)
		So(avoidFlag.DefValue, ShouldEqual, defaultConfig.ManagerQueuesAvoid)
		So(avoidFlag.Usage, ShouldContainSubstring, "replaces")
	})

	Convey("Given manager start's queue flags on the command line", t, func() {
		oldScheduler, oldConfig := scheduler, config
		oldQueue, oldAvoid := managerQueue, managerQueuesAvoid

		defer func() {
			scheduler, config = oldScheduler, oldConfig
			managerQueue, managerQueuesAvoid = oldQueue, oldAvoid
			flags.Lookup(testQueueFlag).Changed = false
			flags.Lookup(testQueuesAvoidFlag).Changed = false
		}()

		config = &internal.Config{Deployment: internal.Development}

		err := flags.Parse([]string{"--" + testQueueFlag, "normal,long", "--" + testQueuesAvoidFlag, "yesterday,"})
		So(err, ShouldBeNil)

		Convey("they reach the lsf scheduler config", func() {
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

			warnIgnoredQueueFlags(managerStartCmd, schedulerLSF)
			So(logged.String(), ShouldNotContainSubstring, testQueueIgnored)
		})

		Convey("they are warned about as ignored with another scheduler", func() {
			logged := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			warnIgnoredQueueFlags(managerStartCmd, "local")
			So(logged.String(), ShouldContainSubstring, "--"+testQueueFlag)
			So(logged.String(), ShouldContainSubstring, "--"+testQueuesAvoidFlag)
			So(logged.String(), ShouldContainSubstring, testQueueIgnored)
		})
	})

	Convey("A single queue flag given with another scheduler is warned about on its own", t, func() {
		defer func() { flags.Lookup(testQueueFlag).Changed = false }()

		oldQueue := managerQueue
		defer func() { managerQueue = oldQueue }()

		So(flags.Set(testQueueFlag, "normal"), ShouldBeNil)

		logged := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		warnIgnoredQueueFlags(managerStartCmd, "local")
		So(logged.String(), ShouldContainSubstring, "--"+testQueueFlag+" only applies to schedulers with queues")
	})

	Convey("Unset queue flags are not warned about with another scheduler", t, func() {
		logged := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		warnIgnoredQueueFlags(managerStartCmd, "openstack")
		So(logged.String(), ShouldNotContainSubstring, testQueueIgnored)
	})
}
