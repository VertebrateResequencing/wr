/*******************************************************************************
 * Copyright (c) 2016-2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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

package cloud

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
)

// destroyServerIfLockedUp waits a couple of seconds then, if the server can no
// longer be ssh'd to (i.e. it has locked up), destroys it. Used by the test
// that simulates a server lock-up by killing its network.
func destroyServerIfLockedUp(ctx context.Context, server *Server) {
	<-time.After(2 * time.Second)

	if server.Alive(ctx, true) {
		return
	}

	if errd := server.Destroy(ctx); errd != nil {
		log.Printf("deferred server.Destroy failed: %s", errd)
	}
}

// TestFlavorSelection tests the cheapest-flavor selection logic directly,
// without needing a real cloud provider (the real openstack flavor lookup +
// server spawning stay in TestOpenStack, gated on OS_* env vars). It feeds
// pickCheapestFromFlavors a constructed set of flavors, exercising the same
// logic the provider uses once it has fetched the real flavors.
func TestFlavorSelection(t *testing.T) {
	Convey("pickCheapestFromFlavors picks the cheapest flavor that meets the requirements", t, func() {
		flavors := map[string]*Flavor{
			"tiny":   {Name: "tiny", Cores: 1, RAM: 1024, Disk: 10},
			"small":  {Name: "small", Cores: 2, RAM: 4096, Disk: 20},
			"small2": {Name: "small2", Cores: 2, RAM: 4096, Disk: 10}, // same cores+RAM as small, less disk
			"medium": {Name: "medium", Cores: 4, RAM: 8192, Disk: 40},
			"large":  {Name: "large", Cores: 8, RAM: 16384, Disk: 80},
		}

		Convey("it picks the smallest flavor that fits the cores and RAM", func() {
			f := pickCheapestFromFlavors(flavors, 1, 512, nil, nil)
			So(f, ShouldNotBeNil)
			So(f.Name, ShouldEqual, "tiny")
		})

		Convey("it skips flavors with too few cores", func() {
			f := pickCheapestFromFlavors(flavors, 3, 512, nil, nil)
			So(f, ShouldNotBeNil)
			So(f.Name, ShouldEqual, "medium")
		})

		Convey("for equal cores and RAM it prefers the flavor with less disk", func() {
			f := pickCheapestFromFlavors(flavors, 2, 4096, nil, nil)
			So(f, ShouldNotBeNil)
			So(f.Name, ShouldEqual, "small2")
		})

		Convey("it returns nil when nothing is big enough", func() {
			f := pickCheapestFromFlavors(flavors, 100, 512, nil, nil)
			So(f, ShouldBeNil)
		})

		Convey("a name regex restricts the candidate flavors", func() {
			f := pickCheapestFromFlavors(flavors, 1, 512, regexp.MustCompile("^large$"), nil)
			So(f, ShouldNotBeNil)
			So(f.Name, ShouldEqual, "large")
		})

		Convey("a subset restricts to flavors matching one of its regexps", func() {
			f := pickCheapestFromFlavors(flavors, 1, 512, nil, []*regexp.Regexp{regexp.MustCompile("medium|large")})
			So(f, ShouldNotBeNil)
			So(f.Name, ShouldEqual, "medium")
		})

		Convey("a nil subset regexp matches any flavor", func() {
			var f *Flavor

			So(func() {
				f = pickCheapestFromFlavors(flavors, 1, 512, nil, []*regexp.Regexp{nil})
			}, ShouldNotPanic)
			So(f, ShouldNotBeNil)
			So(f.Name, ShouldEqual, "tiny")
		})
	})
}

func TestUtility(t *testing.T) {
	Convey("nameToHostName works", t, func() {
		So(nameToHostName("test-123-one"), ShouldEqual, "test-123-one")
		So(nameToHostName("teSt-123-one"), ShouldEqual, "test-123-one")
		So(nameToHostName("test_123-one"), ShouldEqual, "test-123-one")
		So(nameToHostName("test_123*ONE"), ShouldEqual, "test-123-one")
	})
}

func TestOpenStack(t *testing.T) {
	ctx := context.Background()
	osPrefix := os.Getenv("OS_OS_PREFIX")
	osUser := os.Getenv("OS_OS_USERNAME")
	localUser := os.Getenv("OS_LOCAL_USERNAME")
	flavorRegex := os.Getenv("OS_FLAVOR_REGEX")
	ofs := os.Getenv("OS_FLAVOR_SETS")

	var flavorSets [][]string

	if ofs != "" {
		sets := strings.SplitSeq(ofs, ";")
		for set := range sets {
			flavors := strings.Split(set, ",")
			flavorSets = append(flavorSets, flavors)
		}
	}

	host, errh := os.Hostname()
	if errh != nil {
		t.Fatal(errh)
	}

	resourceName := uniqueResourceName("wr-testing-" + localUser)

	if osPrefix == "" || osUser == "" || localUser == "" || flavorRegex == "" {
		SkipConvey("Without our special OS_OS_PREFIX, OS_OS_USERNAME, OS_LOCAL_USERNAME and OS_FLAVOR_REGEX "+
			"environment variables, we'll skip openstack tests", t, func() {})

		return
	}

	crdir, err := os.MkdirTemp("", "wr_testing_cr")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(crdir)

	crfileprefix := filepath.Join(crdir, "resources")

	Convey("You can find out the required environment variables for providers before creating instances "+
		"with New()", t, func() {
		vars, err := RequiredEnv("openstack")
		So(err, ShouldBeNil)
		So(vars, ShouldResemble, expectedOpenStackRequiredEnv())
	})

	Convey("You can find out the possibly required environment variables for providers as well", t, func() {
		vars, err := MaybeEnv("openstack")
		So(err, ShouldBeNil)
		So(vars, ShouldResemble, expectedOpenStackMaybeEnv())
	})

	Convey("And you can get all the env vars in one go", t, func() {
		vars, err := AllEnv("openstack")
		So(err, ShouldBeNil)
		So(vars, ShouldResemble, expectedOpenStackAllEnv())
	})

	if os.Getenv("OS_PROJECT_NAME") != "" && os.Getenv("OS_PROJECT_ID") != "" {
		Convey("You can get a new OpenStack Provider with both OS_PROJECT_NAME and OS_PROJECT_ID set", t, func() {
			p, err := New(ctx, "openstack", resourceName, crfileprefix)
			So(err, ShouldBeNil)
			So(p, ShouldNotBeNil)
		})

		Convey("You can get a new OpenStack Provider with just OS_PROJECT_NAME set", t, func() {
			os.Unsetenv("OS_PROJECT_ID")

			p, err := New(ctx, "openstack", resourceName, crfileprefix)
			So(err, ShouldBeNil)
			So(p, ShouldNotBeNil)
		})
	}

	Convey("You can get a new OpenStack Provider", t, func() {
		p, err := New(ctx, "openstack", resourceName, crfileprefix)
		So(err, ShouldBeNil)
		So(p, ShouldNotBeNil)

		// *** don't know how to test InCloud(), since I don't know if we
		// are in the cloud or not without asking InCloud()! But we make use
		// of the answer to make other tests work properly, so it is
		// indirectly tested
		inCloud := p.InCloud()

		Convey("Debug log contains cloud context type as openstack", func() {
			cctx := p.cloudContext(ctx)
			buff := clog.ToBufferAtLevel("debug")

			clog.Debug(cctx, "msg", "foo", 1)
			So(buff.String(), ShouldContainSubstring, "cloudtype=openstack")
		})

		Convey("You can get your quota details", func() {
			q, err := p.GetQuota(ctx)
			So(err, ShouldBeNil)
			// author only tests, where I know the expected results
			if host == "vr-2-2-02" {
				So(q.MaxCores, ShouldEqual, 446)
				So(q.MaxInstances, ShouldEqual, 446)
				So(q.MaxRAM, ShouldEqual, 3584000)
				// *** gophercloud API doesn't tell us about volume quota :(
				// *** not reliable to try and test for the .Used* values...
			}
		})

		Convey("You can deploy to OpenStack and get the cheapest server flavors", func() {
			err := p.Deploy(ctx, &DeployConfig{RequiredPorts: []int{22}})
			So(err, ShouldBeNil)
			So(p.resources, ShouldNotBeNil)
			So(p.resources.ResourceName, ShouldEqual, resourceName)
			So(p.resources.PrivateKey, ShouldNotBeBlank)
			So(p.PrivateKey(), ShouldEqual, p.resources.PrivateKey)

			So(p.resources.Details["keypair"], ShouldEqual, resourceName)

			if inCloud {
				So(p.resources.Details["secgroup"], ShouldNotBeBlank)
				So(p.resources.Details["network"], ShouldBeBlank)
				So(p.resources.Details["subnet"], ShouldBeBlank)
				So(p.resources.Details["router"], ShouldBeBlank)
			} else {
				So(p.resources.Details["secgroup"], ShouldNotBeBlank)
				So(p.resources.Details["network"], ShouldNotBeBlank)
				So(p.resources.Details["subnet"], ShouldNotBeBlank)
				So(p.resources.Details["router"], ShouldNotBeBlank)
			}

			flavor, err := p.CheapestServerFlavor(ctx, 1, 2048, flavorRegex)
			So(err, ShouldBeNil)
			So(flavor.RAM, ShouldBeGreaterThanOrEqualTo, 2048)
			So(flavor.Disk, ShouldBeGreaterThanOrEqualTo, 1)
			So(flavor.Cores, ShouldBeGreaterThanOrEqualTo, 1)

			// author only tests, where I know the expected results
			if host == "vr-2-2-02" && len(flavorSets) > 1 {
				flavors, err := p.CheapestServerFlavors(ctx, 1, 2048, flavorRegex, flavorSets)
				So(err, ShouldBeNil)
				So(len(flavors), ShouldEqual, 3)
				So(flavors[0].Name, ShouldEqual, "m1.tiny")
				So(flavors[1].Name, ShouldEqual, "m2.tiny")
				So(flavors[2], ShouldBeNil)
			}

			Convey("TearDown deletes all the resources that deploy made", func() {
				err := p.TearDown(ctx)

				if p.InCloud() {
					// the deploy didn't actually create anything that
					// teardown would delete, so it complains
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldContainSubstring, "nothing to tear down")
				} else {
					So(err, ShouldBeNil)
				}

				// *** should really use openstack API to confirm everything is
				// really deleted...
			})

			Convey("Once deployed you can Spawn a server with an external ip", func() {
				_, err := p.Spawn(ctx, "osPrefix", osUser, flavor.ID, 1, 0*time.Second, true)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldEqual, "no OS image with prefix [osPrefix] was found")

				buff := clog.ToBufferAtLevel("debug")

				server, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
				So(err, ShouldBeNil)
				So(server.ID, ShouldNotBeBlank)
				So(server.AdminPass, ShouldNotBeBlank)
				So(server.IP, ShouldNotBeBlank)
				So(server.IP, ShouldNotStartWith, "192")
				So(p.resources.Servers[server.ID], ShouldNotBeNil)
				So(p.resources.Servers[server.ID].IP, ShouldEqual, server.IP)
				So(buff.String(), ShouldContainSubstring, "cloudtype=openstack")

				ok, err := p.ServerIsKnown(server.ID)
				So(err, ShouldBeNil)
				So(ok, ShouldBeTrue)
				// *** negative tests of ServerIsKnown are not possible without mocks, since with the
				// real system we need an alternate set of working credentials

				ok, err = p.CheckServer(ctx, server.ID)
				So(err, ShouldBeNil)
				So(ok, ShouldBeTrue)

				Convey("And you can Spawn another with an internal ip and destroy it with DestroyServer", func() {
					server2, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, false)
					So(err, ShouldBeNil)
					So(server2.ID, ShouldNotBeBlank)
					So(server2.AdminPass, ShouldNotBeBlank)
					So(server2.ID, ShouldNotEqual, server.ID)
					So(server2.AdminPass, ShouldNotEqual, server.AdminPass)
					So(server2.IP, ShouldStartWith, "192")
					So(p.resources.Servers[server2.ID], ShouldBeNil)

					ok, err := p.CheckServer(ctx, server2.ID)
					So(err, ShouldBeNil)
					So(ok, ShouldBeTrue)

					servers := p.Servers()
					So(len(servers), ShouldEqual, 1)
					So(servers[server.ID].IP, ShouldEqual, server.IP)

					err = p.DestroyServer(ctx, server2.ID)
					So(err, ShouldBeNil)

					ok, err = p.CheckServer(ctx, server2.ID)
					So(err, ShouldBeNil)
					So(ok, ShouldBeFalse)
				})
			})

			Convey("Once deployed you can Spawn a server with an internal ip", func() {
				server2, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, false)
				So(err, ShouldBeNil)

				ok, err := p.CheckServer(ctx, server2.ID)
				So(err, ShouldBeNil)
				So(ok, ShouldBeTrue)

				ok = server2.Alive(ctx)
				So(ok, ShouldBeTrue)

				Convey("You can destroy it with Destroy", func() {
					err = server2.Destroy(ctx)
					So(err, ShouldBeNil)

					ok = server2.Alive(ctx)
					So(ok, ShouldBeFalse)
				})
			})

			Convey("Spawn returns a Server object that lets you Allocate, Release and check HasSpaceFor", func() {
				server, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
				So(err, ShouldBeNil)

				defer func() {
					errd := server.Destroy(ctx)
					if errd != nil {
						log.Printf("deferred server.Destroy failed: %s", errd)
					}
				}()

				err = server.WaitUntilReady(context.Background(), "",
					[]byte("#!/bin/bash\nsleep 10 && echo bar > /tmp/post_creation_script_output"))
				So(err, ShouldBeNil)

				ok := server.Alive(ctx, true)
				So(ok, ShouldBeTrue)

				n := server.HasSpaceFor(1, 0, 0)
				So(n, ShouldEqual, flavor.Cores)

				worked := server.Allocate(ctx, float64(flavor.Cores+1), 100, 0)
				So(worked, ShouldEqual, false)
				worked = server.Allocate(ctx, float64(flavor.Cores), 100, 0)
				So(worked, ShouldEqual, true)

				n = server.HasSpaceFor(1, 0, 0)
				So(n, ShouldEqual, 0)

				worked = server.Allocate(ctx, 1, 0, 0)
				So(worked, ShouldEqual, false)

				server.Release(ctx, float64(flavor.Cores), 100, 0)
				n = server.HasSpaceFor(1, 0, 0)
				So(n, ShouldEqual, flavor.Cores)

				n = server.HasSpaceFor(1, flavor.RAM, 0)
				So(n, ShouldEqual, 1)
				n = server.HasSpaceFor(1, flavor.RAM+1, 0)
				So(n, ShouldEqual, 0)

				n = server.HasSpaceFor(1, flavor.RAM, flavor.Disk)
				So(n, ShouldEqual, 1)
				n = server.HasSpaceFor(1, flavor.RAM, flavor.Disk+1)
				So(n, ShouldEqual, 0)

				Convey("You can also interact with the server over ssh, running commands and creating "+
					"files and directories", func() {
					// our post creation script should have completed before WaitUntilReady() returned
					stdout, stderr, err := server.RunCmd(context.Background(), "cat /tmp/post_creation_script_output", false)
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "bar\n")
					So(stderr, ShouldBeBlank)

					err = server.MkDir(context.Background(), "/tmp/foo/bar")
					So(err, ShouldBeNil)

					// *** don't know why ls on its own returns exit code 2...
					stdout, _, err = server.RunCmd(context.Background(), "bash -c ls /tmp/foo/bar", false)
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "")

					err = server.CreateFile(context.Background(), "my content", "/tmp/foo/bar/a/b/file")
					So(err, ShouldBeNil)

					stdout, _, err = server.RunCmd(context.Background(), "cat /tmp/foo/bar/a/b/file", false)
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "my content")

					localFile := filepath.Join(crdir, "source")
					err = os.WriteFile(localFile, []byte("uploadable content"), 0o600)
					So(err, ShouldBeNil)

					err = server.UploadFile(context.Background(), localFile, "/tmp/foo/bar/a/c/file")
					So(err, ShouldBeNil)

					stdout, stderr, err = server.RunCmd(context.Background(), "cat /tmp/foo/bar/a/c/file", false)
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "uploadable content")
					So(stderr, ShouldBeBlank)

					Convey("You can run multiple commands at once and they get cancelled if the server silently locks up", func() {
						// first find out our network interface so we
						// can later simulate a server lock up by killing
						// the network
						intf, _, err := server.RunCmd(context.Background(), "route | grep '^default' | grep -o '[^ ]*$'", false)
						So(err, ShouldBeNil)

						intf = strings.TrimSpace(intf)
						So(intf, ShouldNotBeBlank)

						num := 3

						results := make(chan bool, num)
						for i := 1; i <= num; i++ {
							go func(i int) {
								cmd := "sleep 5"
								if i == num {
									cmd = fmt.Sprintf("sudo ifconfig %s down", intf)

									go destroyServerIfLockedUp(ctx, server)
								}

								_, _, err := server.RunCmd(context.Background(), cmd, false)
								results <- err != nil
							}(i)
						}

						for i := 1; i <= num; i++ {
							So(<-results, ShouldBeTrue)
						}
					})

					Convey("You can run many commands at once without hitting ssh problems", func() {
						num := 30

						results := make(chan bool, num)
						for i := 1; i <= num; i++ {
							go func() {
								_, _, err := server.RunCmd(context.Background(), "sleep 3", false)
								if err != nil {
									results <- false
								} else {
									results <- true
								}
							}()
						}

						for i := 1; i <= num; i++ {
							So(<-results, ShouldBeTrue)
						}
					})
				})
			})

			Convey("Spawning with a bad start up script returns an error, but a live server", func() {
				server, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
				So(err, ShouldBeNil)
				err = server.WaitUntilReady(context.Background(), "", []byte("#!/bin/bash\n>&2 echo foo\nfalse"))
				So(err, ShouldNotBeNil)

				ok := server.Alive(ctx, true)
				So(ok, ShouldBeTrue)
				So(err.Error(), ShouldStartWith, "cloud server script failed: cloud RunCmd(/tmp/.server_script) "+
					"failed: Process exited with status 1\nSTDERR:\nfoo")
				err = server.Destroy(ctx)
				So(err, ShouldBeNil)
			})

			Convey("Spawning with a start up script that takes too long returns an error as well", func() {
				server, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
				So(err, ShouldBeNil)

				pcsTimeOut = 1 * time.Second
				defer func() {
					pcsTimeOut = 15 * time.Minute
				}()

				err = server.WaitUntilReady(context.Background(), "", []byte("#!/bin/bash\nsleep 5"))
				So(err, ShouldNotBeNil)

				ok := server.Alive(ctx, true)
				So(ok, ShouldBeTrue)
				So(err.Error(), ShouldStartWith, "cloud server script failed to complete within")
				err = server.Destroy(ctx)
				So(err, ShouldBeNil)
			})

			Convey("WaitUntilReady can be cancelled", func() {
				server, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
				So(err, ShouldBeNil)

				cancelCtx, cancel := context.WithCancel(context.Background())
				defer cancel()

				go func() {
					<-time.After(2 * time.Second)
					cancel()
				}()

				t := time.Now()
				err = server.WaitUntilReady(cancelCtx, "", []byte("#!/bin/bash\nsleep 5"))
				took := time.Since(t)

				So(err, ShouldNotBeNil)

				ok := server.Alive(cancelCtx, true)
				So(ok, ShouldBeTrue)
				So(err.Error(), ShouldContainSubstring, "cancelled")
				err = server.Destroy(cancelCtx)
				So(err, ShouldBeNil)
				So(took, ShouldBeGreaterThanOrEqualTo, 2*time.Second)
				So(took, ShouldBeLessThan, 3*time.Second)
			})

			Convey("Spawning with a start up script that relies on an unsupplied file returns an error", func() {
				server, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
				So(err, ShouldBeNil)
				err = server.WaitUntilReady(context.Background(), "", []byte("#!/bin/bash\ncat /tmp/foo"))
				So(err, ShouldNotBeNil)

				ok := server.Alive(ctx, true)
				So(ok, ShouldBeTrue)
				So(err.Error(), ShouldStartWith, "cloud server script failed: cloud RunCmd(/tmp/.server_script) "+
					"failed: Process exited with status 1")
				err = server.Destroy(ctx)
				So(err, ShouldBeNil)

				Convey("But supplying the file makes it work", func() {
					server, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
					So(err, ShouldBeNil)

					_, filename, _, _ := runtime.Caller(0)
					err = server.WaitUntilReady(context.Background(), filename+":/tmp/foo", []byte("#!/bin/bash\ncat /tmp/foo"))
					So(err, ShouldBeNil)

					ok := server.Alive(ctx, true)
					So(ok, ShouldBeTrue)

					err = server.Destroy(ctx)
					So(err, ShouldBeNil)
				})
			})

			Convey("You can Spawn a server with a time to destruction", func() {
				server3, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, 1, 2*time.Second, false)
				So(err, ShouldBeNil)

				ok := server3.Alive(ctx)
				So(ok, ShouldBeTrue)

				ok = server3.Destroyed()
				So(ok, ShouldBeFalse)

				<-time.After(3 * time.Second)

				ok = server3.Alive(ctx)
				So(ok, ShouldBeTrue)

				server3.Allocate(ctx, 1, 100, 0)
				server3.Release(ctx, 1, 100, 0)
				<-time.After(1 * time.Second)
				server3.Allocate(ctx, 1, 100, 0)
				<-time.After(2 * time.Second)

				ok = server3.Alive(ctx)
				So(ok, ShouldBeTrue)

				server3.Allocate(ctx, 0, 100, 0)
				server3.Release(ctx, 0, 100, 0)

				<-time.After(3 * time.Second)

				ok = server3.Alive(ctx)
				So(ok, ShouldBeTrue)

				server3.Release(ctx, 1, 100, 0)

				<-time.After(3 * time.Second)

				ok = server3.Alive(ctx)
				So(ok, ShouldBeFalse)

				ok = server3.Destroyed()
				So(ok, ShouldBeTrue)

				ok, err = p.CheckServer(ctx, server3.ID)
				So(err, ShouldBeNil)
				So(ok, ShouldBeFalse)
			})

			Convey("You can't get a server flavor when your requirements are crazy", func() {
				var perr Error

				_, err := p.CheapestServerFlavor(ctx, 20, 9999999999, flavorRegex)
				So(err, ShouldNotBeNil)
				So(errors.As(err, &perr), ShouldBeTrue)
				So(perr.Err, ShouldEqual, ErrNoFlavor)
			})

			Convey("You can't get a server flavor when your regex is bad, but can when it is good", func() {
				var perr Error

				flavor2, err := p.CheapestServerFlavor(ctx, 1, 50, "^!!!!!!!!!!!!!!$")
				So(err, ShouldNotBeNil)
				So(flavor2, ShouldBeNil)
				So(errors.As(err, &perr), ShouldBeTrue)
				So(perr.Err, ShouldEqual, ErrNoFlavor)

				flavor2, err = p.CheapestServerFlavor(ctx, 1, 50, "^!!!!(")
				So(err, ShouldNotBeNil)
				So(flavor2, ShouldBeNil)
				So(errors.As(err, &perr), ShouldBeTrue)
				So(perr.Err, ShouldEqual, ErrBadRegex)

				flavor2, err = p.CheapestServerFlavor(ctx, 1, 50, ".*$")
				So(err, ShouldBeNil)
				So(flavor2, ShouldNotBeNil)
			})

			Convey("You can Spawn a server with additional disk space over the default for the desired image", func() {
				server, err := p.Spawn(ctx, osPrefix, osUser, flavor.ID, flavor.Disk+10, 0*time.Second, true)
				So(err, ShouldBeNil)

				ok := server.Alive(ctx, true)
				So(ok, ShouldBeTrue)

				stdout, _, err := server.RunCmd(context.Background(), "df -h .", false)
				So(err, ShouldBeNil)
				So(stdout, ShouldContainSubstring, fmt.Sprintf("%dG", flavor.Disk+10))
			})

			Reset(func() {
				errd := p.TearDown(ctx)
				if errd != nil && !strings.Contains(errd.Error(), "nothing to tear down") {
					log.Printf("reset p.Teardown failed: %s", errd)
				}
			})
		})

		// *** we need all the tests for negative and failure cases

		errd := p.TearDown(ctx)
		if errd != nil && !strings.Contains(errd.Error(), "nothing to tear down") {
			log.Printf("ending p.Teardown failed: %s", errd)
		}
	})
}
