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
)

const resourceName = "wr_testing"
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

			Convey("You can deploy to OpenStack", func() {
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

				Convey("Once deployed you can Spawn a server with an external ip", func() {
					flavor, ramMB, diskGB, CPUs, err := p.CheapestServerFlavor(2048, 20, 1)
					So(err, ShouldBeNil)
					So(ramMB, ShouldBeGreaterThanOrEqualTo, 2048)
					So(diskGB, ShouldBeGreaterThanOrEqualTo, 20)
					So(CPUs, ShouldBeGreaterThanOrEqualTo, 1)

					serverID, serverIP, adminPass, err := p.Spawn(osPrefix, flavor, true)
					So(err, ShouldBeNil)
					So(serverID, ShouldNotBeBlank)
					So(adminPass, ShouldNotBeBlank)
					So(serverIP, ShouldNotBeBlank)
					So(serverIP, ShouldNotStartWith, "192")
					So(p.resources.Servers[serverID], ShouldNotBeNil)
					So(p.resources.Servers[serverID], ShouldEqual, serverIP)

					ok, err := p.CheckServer(serverID)
					So(err, ShouldBeNil)
					So(ok, ShouldBeTrue)

					Convey("And you can Spawn another with an internal ip", func() {
						serverID2, serverIP2, adminPass2, err := p.Spawn(osPrefix, flavor, false)
						So(err, ShouldBeNil)
						So(serverID2, ShouldNotBeBlank)
						So(adminPass2, ShouldNotBeBlank)
						So(serverID2, ShouldNotEqual, serverID)
						So(adminPass2, ShouldNotEqual, adminPass)
						So(serverIP2, ShouldStartWith, "192")
						So(p.resources.Servers[serverID2], ShouldBeBlank)

						ok, err := p.CheckServer(serverID2)
						So(err, ShouldBeNil)
						So(ok, ShouldBeTrue)

						servers := p.Servers()
						So(servers, ShouldResemble, map[string]string{serverID: serverIP})

						Convey("Then you can destroy it", func() {
							err = p.DestroyServer(serverID2)
							So(err, ShouldBeNil)

							ok, err = p.CheckServer(serverID2)
							So(err, ShouldBeNil)
							So(ok, ShouldBeFalse)
						})
					})

					Convey("But you can't even get a server flavor when your requirements are crazy", func() {
						_, _, _, _, err := p.CheapestServerFlavor(9999999999, 20, 9999999)
						So(err, ShouldNotBeNil)
						perr, ok := err.(Error)
						So(ok, ShouldBeTrue)
						So(perr.Err, ShouldEqual, ErrNoFlavor)
					})
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
