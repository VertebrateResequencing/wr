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
	"os"
	"testing"
)

const resourceName = "wr_testing"

func TestOpenStack(t *testing.T) {
	osPrefix := os.Getenv("OS_OS_PREFIX")

	if osPrefix == "" {
		SkipConvey("Without our special OS_OS_PREFIX environment variable, we'll skip openstack tests", t, func() {})
	} else {
		Convey("You can get a new OpenStack Provider", t, func() {
			p, err := New("openstack", resourceName)
			So(err, ShouldBeNil)
			So(p, ShouldNotBeNil)

			Convey("You can deploy to OpenStack", func() {
				resources, err := p.Deploy([]int{22})
				So(err, ShouldBeNil)
				So(resources, ShouldNotBeNil)
				So(resources.NamePrefix, ShouldEqual, resourceName)
				So(resources.PrivateKey, ShouldNotEqual, "")

				So(resources.Details["keypair"], ShouldEqual, resourceName)
				So(resources.Details["secgroup"], ShouldNotEqual, "")
				So(resources.Details["network"], ShouldNotEqual, "")
				So(resources.Details["subnet"], ShouldNotEqual, "")
				So(resources.Details["router"], ShouldNotEqual, "")

				Convey("Once deployed you can Spawn a server with an external ip", func() {
					serverID, serverIP, adminPass, err := p.Spawn(osPrefix, 2048, 20, 1, true)
					So(err, ShouldBeNil)
					So(serverID, ShouldNotEqual, "")
					So(adminPass, ShouldNotEqual, "")
					So(serverIP, ShouldNotStartWith, "192")

					Convey("And you can Spawn another with an internal ip", func() {
						serverID2, serverIP2, adminPass2, err := p.Spawn(osPrefix, 2048, 20, 1, false)
						So(err, ShouldBeNil)
						So(serverID2, ShouldNotEqual, "")
						So(adminPass2, ShouldNotEqual, "")
						So(serverID2, ShouldNotEqual, serverID)
						So(adminPass2, ShouldNotEqual, adminPass)
						So(serverIP2, ShouldStartWith, "192")

						Convey("Then you can destroy it", func() {
							err = p.DestroyServer(serverID)
							So(err, ShouldBeNil)
						})
					})
				})

				Convey("TearDown deletes all the resources that deploy made", func() {
					err = p.TearDown(resources)
					So(err, ShouldBeNil)

					// *** should really use openstack API to confirm everything is
					// really deleted...
				})

				Reset(func() {
					p.TearDown(resources)
				})
			})

			// *** we need all the tests for negative and failure cases
		})
	}
}
