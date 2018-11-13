// Copyright © 2016-2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package cloud

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	. "github.com/smartystreets/goconvey/convey"
)

var testLogger = log15.New()

func init() {
	testLogger.SetHandler(log15.LvlFilterHandler(log15.LvlWarn, log15.StderrHandler))
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
	osPrefix := os.Getenv("OS_OS_PREFIX")
	osUser := os.Getenv("OS_OS_USERNAME")
	localUser := os.Getenv("OS_LOCAL_USERNAME")
	flavorRegex := os.Getenv("OS_FLAVOR_REGEX")
	resourceName := "wr-testing-" + localUser

	if osPrefix == "" || osUser == "" || localUser == "" || flavorRegex == "" {
		SkipConvey("Without our special OS_OS_PREFIX, OS_OS_USERNAME, OS_LOCAL_USERNAME and OS_FLAVOR_REGEX environment variables, we'll skip openstack tests", t, func() {})
	} else {
		crdir, err := ioutil.TempDir("", "wr_testing_cr")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(crdir)
		crfileprefix := filepath.Join(crdir, "resources")

		Convey("You can find out the required environment variables for providers before creating instances with New()", t, func() {
			vars, err := RequiredEnv("openstack")
			So(err, ShouldBeNil)
			So(vars, ShouldResemble, []string{"OS_AUTH_URL", "OS_USERNAME", "OS_PASSWORD", "OS_REGION_NAME"})
		})

		Convey("You can find out the possibly required environment variables for providers as well", t, func() {
			vars, err := MaybeEnv("openstack")
			So(err, ShouldBeNil)
			So(vars, ShouldResemble, []string{"OS_USERID", "OS_TENANT_ID", "OS_TENANT_NAME", "OS_DOMAIN_ID", "OS_PROJECT_DOMAIN_ID", "OS_DOMAIN_NAME", "OS_USER_DOMAIN_NAME", "OS_PROJECT_ID", "OS_PROJECT_NAME", "OS_POOL_NAME"})
		})

		Convey("And you can get all the env vars in one go", t, func() {
			vars, err := AllEnv("openstack")
			So(err, ShouldBeNil)
			So(vars, ShouldResemble, []string{"OS_AUTH_URL", "OS_USERNAME", "OS_PASSWORD", "OS_REGION_NAME", "OS_USERID", "OS_TENANT_ID", "OS_TENANT_NAME", "OS_DOMAIN_ID", "OS_PROJECT_DOMAIN_ID", "OS_DOMAIN_NAME", "OS_USER_DOMAIN_NAME", "OS_PROJECT_ID", "OS_PROJECT_NAME", "OS_POOL_NAME"})
		})

		Convey("You can get a new OpenStack Provider", t, func() {
			p, err := New("openstack", resourceName, crfileprefix, testLogger)
			So(err, ShouldBeNil)
			So(p, ShouldNotBeNil)

			// *** don't know how to test InCloud(), since I don't know if we
			// are in the cloud or not without asking InCloud()! But we make use
			// of the answer to make other tests work properly, so it is
			// indirectly tested
			inCloud := p.InCloud()

			Convey("You can get your quota details", func() {
				q, err := p.GetQuota()
				So(err, ShouldBeNil)
				// author only tests, where I know the expected results
				host, _ := os.Hostname()
				if host == "vr-2-2-02" {
					So(q.MaxCores, ShouldEqual, 446)
					So(q.MaxInstances, ShouldEqual, 446)
					So(q.MaxRAM, ShouldEqual, 3584000)
					//*** gophercloud API doesn't tell us about volume quota :(
					//*** not reliable to try and test for the .Used* values...
				}
			})

			Convey("You can deploy to OpenStack and get the cheapest server flavor", func() {
				err := p.Deploy(&DeployConfig{RequiredPorts: []int{22}})
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

				flavor, err := p.CheapestServerFlavor(1, 2048, flavorRegex)
				So(err, ShouldBeNil)
				So(flavor.RAM, ShouldBeGreaterThanOrEqualTo, 2048)
				So(flavor.Disk, ShouldBeGreaterThanOrEqualTo, 1)
				So(flavor.Cores, ShouldBeGreaterThanOrEqualTo, 1)

				Convey("Once deployed you can Spawn a server with an external ip", func() {
					server, err := p.Spawn("osPrefix", osUser, flavor.ID, 1, 0*time.Second, true)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldEqual, "no OS image with prefix [osPrefix] was found")

					server, err = p.Spawn(osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
					So(err, ShouldBeNil)
					So(server.ID, ShouldNotBeBlank)
					So(server.AdminPass, ShouldNotBeBlank)
					So(server.IP, ShouldNotBeBlank)
					So(server.IP, ShouldNotStartWith, "192")
					So(p.resources.Servers[server.ID], ShouldNotBeNil)
					So(p.resources.Servers[server.ID].IP, ShouldEqual, server.IP)

					ok, err := p.CheckServer(server.ID)
					So(err, ShouldBeNil)
					So(ok, ShouldBeTrue)

					Convey("And you can Spawn another with an internal ip and destroy it with DestroyServer", func() {
						server2, err := p.Spawn(osPrefix, osUser, flavor.ID, 1, 0*time.Second, false)
						So(err, ShouldBeNil)
						So(server2.ID, ShouldNotBeBlank)
						So(server2.AdminPass, ShouldNotBeBlank)
						So(server2.ID, ShouldNotEqual, server.ID)
						So(server2.AdminPass, ShouldNotEqual, server.AdminPass)
						So(server2.IP, ShouldStartWith, "192")
						So(p.resources.Servers[server2.ID], ShouldBeNil)

						ok, err := p.CheckServer(server2.ID)
						So(err, ShouldBeNil)
						So(ok, ShouldBeTrue)

						servers := p.Servers()
						So(len(servers), ShouldEqual, 1)
						So(servers[server.ID].IP, ShouldEqual, server.IP)

						err = p.DestroyServer(server2.ID)
						So(err, ShouldBeNil)

						ok, err = p.CheckServer(server2.ID)
						So(err, ShouldBeNil)
						So(ok, ShouldBeFalse)
					})
				})

				Convey("Once deployed you can Spawn a server with an internal ip", func() {
					server2, err := p.Spawn(osPrefix, osUser, flavor.ID, 1, 0*time.Second, false)
					So(err, ShouldBeNil)

					ok, err := p.CheckServer(server2.ID)
					So(err, ShouldBeNil)
					So(ok, ShouldBeTrue)

					ok = server2.Alive()
					So(ok, ShouldBeTrue)

					Convey("You can destroy it with Destroy", func() {
						err = server2.Destroy()
						So(err, ShouldBeNil)

						ok = server2.Alive()
						So(ok, ShouldBeFalse)
					})
				})

				Convey("Spawn returns a Server object that lets you Allocate, Release and check HasSpaceFor", func() {
					server, err := p.Spawn(osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
					So(err, ShouldBeNil)
					defer server.Destroy()
					err = server.WaitUntilReady("", []byte("#!/bin/bash\nsleep 10 && echo bar > /tmp/post_creation_script_output"))
					So(err, ShouldBeNil)
					ok := server.Alive(true)
					So(ok, ShouldBeTrue)

					n := server.HasSpaceFor(1, 0, 0)
					So(n, ShouldEqual, flavor.Cores)

					server.Allocate(float64(flavor.Cores), 100, 0)
					n = server.HasSpaceFor(1, 0, 0)
					So(n, ShouldEqual, 0)

					server.Release(float64(flavor.Cores), 100, 0)
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

					Convey("You can also interact with the server over ssh, running commands and creating files and directories", func() {
						// our post creation script should have completed before WaitUntilReady() returned
						stdout, stderr, err := server.RunCmd("cat /tmp/post_creation_script_output", false)
						So(err, ShouldBeNil)
						So(stdout, ShouldEqual, "bar\n")
						So(stderr, ShouldBeBlank)

						err = server.MkDir("/tmp/foo/bar")
						So(err, ShouldBeNil)

						stdout, _, err = server.RunCmd("bash -c ls /tmp/foo/bar", false) // *** don't know why ls on its own returns exit code 2...
						So(err, ShouldBeNil)
						So(stdout, ShouldEqual, "")

						err = server.CreateFile("my content", "/tmp/foo/bar/a/b/file")
						So(err, ShouldBeNil)

						stdout, _, err = server.RunCmd("cat /tmp/foo/bar/a/b/file", false)
						So(err, ShouldBeNil)
						So(stdout, ShouldEqual, "my content")

						localFile := filepath.Join(crdir, "source")
						err = ioutil.WriteFile(localFile, []byte("uploadable content"), 0644)
						So(err, ShouldBeNil)

						err = server.UploadFile(localFile, "/tmp/foo/bar/a/c/file")
						So(err, ShouldBeNil)

						stdout, stderr, err = server.RunCmd("cat /tmp/foo/bar/a/c/file", false)
						So(err, ShouldBeNil)
						So(stdout, ShouldEqual, "uploadable content")
						So(stderr, ShouldBeBlank)

						Convey("You can run multiple commands at once and they get cancelled if the server silently locks up", func() {
							// first find out our network interface so we
							// can later simulate a server lock up by killing
							// the network
							intf, _, err := server.RunCmd("route | grep '^default' | grep -o '[^ ]*$'", false)
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
										go func() {
											<-time.After(2 * time.Second)
											alive := server.Alive(true)
											if !alive {
												server.Destroy()
											}
										}()
									}
									_, _, err := server.RunCmd(cmd, false)
									if err != nil {
										results <- true
									} else {
										results <- false
									}
								}(i)
							}

							for i := 1; i <= num; i++ {
								So(<-results, ShouldBeTrue)
							}
						})
					})
				})

				Convey("Spawning with a bad start up script returns an error, but a live server", func() {
					server, err := p.Spawn(osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
					So(err, ShouldBeNil)
					err = server.WaitUntilReady("", []byte("#!/bin/bash\nfalse"))
					So(err, ShouldNotBeNil)
					ok := server.Alive(true)
					So(ok, ShouldBeTrue)
					So(err.Error(), ShouldStartWith, "cloud server start up script failed: cloud RunCmd(/tmp/.postCreationScript) failed: Process exited with status 1")
					server.Destroy()
				})

				Convey("Spawning with a start up script that relies on an unsupplied file returns an error", func() {
					server, err := p.Spawn(osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
					So(err, ShouldBeNil)
					err = server.WaitUntilReady("", []byte("#!/bin/bash\ncat /tmp/foo"))
					So(err, ShouldNotBeNil)
					ok := server.Alive(true)
					So(ok, ShouldBeTrue)
					So(err.Error(), ShouldStartWith, "cloud server start up script failed: cloud RunCmd(/tmp/.postCreationScript) failed: Process exited with status 1")
					server.Destroy()

					Convey("But supplying the file makes it work", func() {
						server, err := p.Spawn(osPrefix, osUser, flavor.ID, 1, 0*time.Second, true)
						So(err, ShouldBeNil)
						_, filename, _, _ := runtime.Caller(0)
						err = server.WaitUntilReady(filename+":/tmp/foo", []byte("#!/bin/bash\ncat /tmp/foo"))
						So(err, ShouldBeNil)
						ok := server.Alive(true)
						So(ok, ShouldBeTrue)
						server.Destroy()
					})
				})

				Convey("You can Spawn a server with a time to destruction", func() {
					server3, err := p.Spawn(osPrefix, osUser, flavor.ID, 1, 2*time.Second, false)
					So(err, ShouldBeNil)

					ok := server3.Alive()
					So(ok, ShouldBeTrue)

					ok = server3.Destroyed()
					So(ok, ShouldBeFalse)

					<-time.After(3 * time.Second)

					ok = server3.Alive()
					So(ok, ShouldBeTrue)

					server3.Allocate(1, 100, 0)
					server3.Release(1, 100, 0)
					<-time.After(1 * time.Second)
					server3.Allocate(1, 100, 0)
					<-time.After(2 * time.Second)

					ok = server3.Alive()
					So(ok, ShouldBeTrue)

					server3.Allocate(1, 100, 0)
					server3.Release(1, 100, 0)

					<-time.After(3 * time.Second)

					ok = server3.Alive()
					So(ok, ShouldBeTrue)

					server3.Release(1, 100, 0)

					<-time.After(3 * time.Second)

					ok = server3.Alive()
					So(ok, ShouldBeFalse)

					ok = server3.Destroyed()
					So(ok, ShouldBeTrue)

					ok, err = p.CheckServer(server3.ID)
					So(err, ShouldBeNil)
					So(ok, ShouldBeFalse)
				})

				Convey("You can't get a server flavor when your requirements are crazy", func() {
					_, err := p.CheapestServerFlavor(20, 9999999999, flavorRegex)
					So(err, ShouldNotBeNil)
					perr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(perr.Err, ShouldEqual, ErrNoFlavor)
				})

				Convey("You can't get a server flavor when your regex is bad, but can when it is good", func() {
					flavor2, err := p.CheapestServerFlavor(1, 50, "^!!!!!!!!!!!!!!$")
					So(err, ShouldNotBeNil)
					So(flavor2, ShouldBeNil)
					perr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(perr.Err, ShouldEqual, ErrNoFlavor)

					flavor2, err = p.CheapestServerFlavor(1, 50, "^!!!!(")
					So(err, ShouldNotBeNil)
					So(flavor2, ShouldBeNil)
					perr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(perr.Err, ShouldEqual, ErrBadRegex)

					flavor2, err = p.CheapestServerFlavor(1, 50, ".*$")
					So(err, ShouldBeNil)
					So(flavor2, ShouldNotBeNil)
				})

				Convey("You can Spawn a server with additional disk space over the default for the desired image", func() {
					server, err := p.Spawn(osPrefix, osUser, flavor.ID, flavor.Disk+10, 0*time.Second, true)
					So(err, ShouldBeNil)
					ok := server.Alive(true)
					So(ok, ShouldBeTrue)

					stdout, _, err := server.RunCmd("df -h .", false)
					So(err, ShouldBeNil)
					So(stdout, ShouldContainSubstring, fmt.Sprintf("%dG", flavor.Disk+10))
				})

				Convey("TearDown deletes all the resources that deploy made", func() {
					err := p.TearDown()
					So(err, ShouldBeNil)

					// *** should really use openstack API to confirm everything is
					// really deleted...
				})

				Reset(func() {
					p.TearDown()
				})
			})

			// *** we need all the tests for negative and failure cases

			p.TearDown()
		})
	}
}
