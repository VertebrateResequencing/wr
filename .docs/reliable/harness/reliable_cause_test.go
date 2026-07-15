/*******************************************************************************
 * TEMP reliability reproduction (not for merge). Targets the CAUSE of false
 * lost-contact that #548 does NOT fix: a still-running job that is being touched
 * on schedule is nonetheless marked Lost because a saturated manager processes
 * its touch after the TTR deadline (item.touch() resets releaseAt only when the
 * touch is actually processed; the TTR sweeper fires off the last processed
 * touch). #548 only rescues the late successful ARCHIVE; it does not stop the
 * job being flipped to Lost while alive and touching.
 *
 * This is a STRESS reproduction: it saturates the manager with many concurrent
 * clients while a dedicated goroutine touches a protected running job well
 * within its TTR, and asserts the protected job is never observed Lost.
 ******************************************************************************/

package jobqueue

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestReliableFalseLostUnderSaturation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr          = 500 * time.Millisecond
		touchEvery   = 120 * time.Millisecond // ~4x margin within the TTR
		saturators   = 120                    // concurrent client connections hammering the manager
		runFor       = 12 * time.Second
		poolRepGroup = "reliable_saturation_pool"
		protRepGroup = "reliable_saturation_protected"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A running job touched well within its TTR must never be marked Lost, even under manager saturation", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		conn := func() *Client {
			c, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			return c
		}

		main := conn()
		defer disconnect(main)

		// protected running job we will touch on schedule.
		protected := &Job{
			Cmd: restFormTrue + " protected", Cwd: testCwdPath, RepGroup: protRepGroup,
			ReqGroup: protRepGroup, Requirements: standardReqs, Retries: 30,
		}
		_, _, err = main.Add([]*Job{protected}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, err := main.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(main.Started(reserved, os.Getpid()), ShouldBeNil)
		protectedKey := reserved.Key()

		stop := make(chan struct{})

		var wg sync.WaitGroup

		// dedicated toucher: touches the protected job every touchEvery (well
		// within ttr). Under a healthy manager this keeps it alive indefinitely.
		toucher := conn()
		defer disconnect(toucher)

		wg.Add(1)

		go func() {
			defer wg.Done()

			tk := time.NewTicker(touchEvery)
			defer tk.Stop()

			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					toucher.Touch(reserved) //nolint:errcheck
				}
			}
		}()

		// observer: samples the protected job's Lost flag straight from the
		// server (no RPC, so it is unaffected by the saturation).
		var everLost int64

		wg.Add(1)

		go func() {
			defer wg.Done()

			tk := time.NewTicker(20 * time.Millisecond)
			defer tk.Stop()

			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					item, errg := server.q.Get(protectedKey)
					if errg != nil || item == nil {
						continue
					}

					job, ok := item.Data().(*Job)
					if !ok {
						continue
					}

					job.RLock()
					lost := job.Lost
					exited := job.Exited
					job.RUnlock()

					if lost && !exited {
						atomic.StoreInt64(&everLost, 1)
					}
				}
			}
		}()

		// saturation: many client connections hammering the manager so touch
		// processing is delayed past the TTR.
		var addCounter int64

		for range saturators {
			wg.Add(1)

			go func() {
				defer wg.Done()

				c, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				if errc != nil {
					return
				}
				defer c.Disconnect()

				for {
					select {
					case <-stop:
						return
					default:
						n := atomic.AddInt64(&addCounter, 1)
						j := &Job{
							Cmd:          restFormTrue + " sat" + itoaCause(int(n)),
							Cwd:          testCwdPath,
							RepGroup:     poolRepGroup,
							ReqGroup:     poolRepGroup,
							Requirements: standardReqs,
							Retries:      0,
						}
						c.Add([]*Job{j}, os.Environ(), true) //nolint:errcheck
						c.GetByRepGroup(poolRepGroup, false, 0, "", false, false) //nolint:errcheck
					}
				}
			}()
		}

		time.Sleep(runFor)
		close(stop)
		wg.Wait()

		// final direct check too
		item, errg := server.q.Get(protectedKey)
		So(errg, ShouldBeNil)
		finalJob, _ := item.Data().(*Job)
		finalJob.RLock()
		finalLost := finalJob.Lost
		finalJob.RUnlock()

		t.Logf("RESULT everLost=%d finalLost=%v saturationAdds=%d",
			atomic.LoadInt64(&everLost), finalLost, atomic.LoadInt64(&addCounter))

		// The protected job was alive and touched every %v within a %v TTR; it
		// must never have been flipped to Lost. If it was, the saturated manager
		// falsely lost a running job (the cause #548 does not address).
		So(atomic.LoadInt64(&everLost), ShouldEqual, 0)
	})
}

func itoaCause(i int) string {
	if i == 0 {
		return "0"
	}

	var b [20]byte

	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}

	return string(b[pos:])
}
