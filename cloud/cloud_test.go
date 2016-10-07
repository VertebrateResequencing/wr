// Copyright © 2016 Genome Research Limited
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
	. "github.com/smartystreets/goconvey/convey"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const resourceName = "wr-testing"
const crfile = "cloud.resources"

func TestOpenStack(t *testing.T) {
	osPrefix := os.Getenv("OS_OS_PREFIX")

	if osPrefix == "" {
		SkipConvey("Without our special OS_OS_PREFIX environment variable, we'll skip openstack tests", t, func() {})
	} else {
		crdir, err := ioutil.TempDir("", "wr_testing_cr")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(crdir)
		crfileprefix := filepath.Join(crdir, "resources")

		Convey("You can get a new OpenStack Provider", t, func() {
			p, err := New("openstack", resourceName, crfileprefix)
			So(err, ShouldBeNil)
			So(p, ShouldNotBeNil)

			Convey("You can get your quota details", func() {
				q, err := p.GetQuota()
				So(err, ShouldBeNil)
				// author only tests, where I know the expected results
				host, _ := os.Hostname()
				if host == "vr-2-2-02" {
					So(q.MaxCores, ShouldEqual, 20)
					So(q.MaxInstances, ShouldEqual, 10)
					So(q.MaxRam, ShouldEqual, 4096)
					//*** not reliable to try and test for the .Used* values...
				}
			})

			Convey("You can deploy to OpenStack and get the cheapest server flavor", func() {
				err := p.Deploy([]int{22})
				So(err, ShouldBeNil)
				So(p.resources, ShouldNotBeNil)
				So(p.resources.ResourceName, ShouldEqual, resourceName)
				So(p.resources.PrivateKey, ShouldNotBeBlank)
				So(p.PrivateKey(), ShouldEqual, p.resources.PrivateKey)

				So(p.resources.Details["keypair"], ShouldEqual, resourceName)
				So(p.resources.Details["secgroup"], ShouldNotBeBlank)
				So(p.resources.Details["network"], ShouldNotBeBlank)
				So(p.resources.Details["subnet"], ShouldNotBeBlank)
				So(p.resources.Details["router"], ShouldNotBeBlank)

				flavor, err := p.CheapestServerFlavor(1, 512, 1)
				So(err, ShouldBeNil)
				So(flavor.RAM, ShouldBeGreaterThanOrEqualTo, 512)
				So(flavor.Disk, ShouldBeGreaterThanOrEqualTo, 1)
				So(flavor.Cores, ShouldBeGreaterThanOrEqualTo, 1)

				Convey("Once deployed you can Spawn a server with an external ip", func() {
					server, err := p.Spawn(osPrefix, flavor.ID, 0*time.Second, true)
					So(err, ShouldBeNil)
					So(server.ID, ShouldNotBeBlank)
					So(server.AdminPass, ShouldNotBeBlank)
					So(server.IP, ShouldNotBeBlank)
					So(server.IP, ShouldNotStartWith, "192")
					So(p.resources.Servers[server.ID], ShouldNotBeNil)
					So(p.resources.Servers[server.ID], ShouldEqual, server.IP)

					ok, err := p.CheckServer(server.ID)
					So(err, ShouldBeNil)
					So(ok, ShouldBeTrue)

					Convey("And you can Spawn another with an internal ip and destroy it with DestroyServer", func() {
						server2, err := p.Spawn(osPrefix, flavor.ID, 0*time.Second, false)
						So(err, ShouldBeNil)
						So(server2.ID, ShouldNotBeBlank)
						So(server2.AdminPass, ShouldNotBeBlank)
						So(server2.ID, ShouldNotEqual, server.ID)
						So(server2.AdminPass, ShouldNotEqual, server.AdminPass)
						So(server2.IP, ShouldStartWith, "192")
						So(p.resources.Servers[server2.ID], ShouldBeBlank)

						ok, err := p.CheckServer(server2.ID)
						So(err, ShouldBeNil)
						So(ok, ShouldBeTrue)

						servers := p.Servers()
						So(servers, ShouldResemble, map[string]string{server.ID: server.IP})

						err = p.DestroyServer(server2.ID)
						So(err, ShouldBeNil)

						ok, err = p.CheckServer(server2.ID)
						So(err, ShouldBeNil)
						So(ok, ShouldBeFalse)
					})
				})

				Convey("Once deployed you can Spawn a server with an internal ip", func() {
					server2, err := p.Spawn(osPrefix, flavor.ID, 0*time.Second, false)
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
					server, err := p.Spawn(osPrefix, flavor.ID, 0*time.Second, false)
					So(err, ShouldBeNil)
					ok := server.Alive()
					So(ok, ShouldBeTrue)

					n := server.HasSpaceFor(1, 0, 0)
					So(n, ShouldEqual, flavor.Cores)

					server.Allocate(flavor.Cores, 100, 0)
					n = server.HasSpaceFor(1, 0, 0)
					So(n, ShouldEqual, 0)

					server.Release(flavor.Cores, 100, 0)
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

					server.Destroy()
				})

				Convey("And you can Spawn one with a time to destruction", func() {
					server3, err := p.Spawn(osPrefix, flavor.ID, 2*time.Second, false)
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
					_, err := p.CheapestServerFlavor(20, 9999999999, 9999999)
					So(err, ShouldNotBeNil)
					perr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(perr.Err, ShouldEqual, ErrNoFlavor)
				})

				Convey("TearDown deletes all the resources that deploy made", func() {
					err = p.TearDown()
					So(err, ShouldBeNil)

					// *** should really use openstack API to confirm everything is
					// really deleted...
				})

				Reset(func() {
					p.TearDown()
				})
			})

			// *** we need all the tests for negative and failure cases
		})
	}
}
