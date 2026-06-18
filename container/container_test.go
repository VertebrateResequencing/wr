/*******************************************************************************
 * Copyright (c) 2020 Genome Research Ltd.
 *
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to
 * deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
 * sell copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
 * FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
 * IN THE SOFTWARE.
 ******************************************************************************/

package container

import (
	"context"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestContainer(t *testing.T) {
	ctx := context.Background()

	Convey("Given a NewOperator", t, func() {
		// Create a list of dummy container
		cntrList := []*Container{{
			ID: "container_id1", Names: []string{"/test_container1"},
		}}

		// Create a client with list of dummy containers
		newCntrOperator := NewOperator(&MockInteractor{
			ContainerStatsFn: func(containerID string) (*Stats, error) {
				return &Stats{}, nil
			},
		},
		)

		// add client to the container
		newCntrOperator.addClientToContainers(cntrList)

		// Create a client with no containers
		empNewCntrOperator := NewOperator(&MockInteractor{
			ContainerStatsFn: func(containerID string) (*Stats, error) {
				return nil, &OperatorError{Type: ErrContainerStats}
			},
		},
		)

		Convey("and a container, it can get its stats", func() {
			Convey("for a client with a non-empty list of containers", func() {
				stats, err := cntrList[0].Stats(ctx)
				So(stats.MemoryMB, ShouldBeZeroValue)
				So(stats.CPUSec, ShouldBeZeroValue)
				So(err, ShouldBeNil)
			})

			Convey("not for a client with an empty list of containers", func() {
				// add client to the container
				empNewCntrOperator.addClientToContainers(cntrList)

				stats, err := cntrList[0].Stats(ctx)
				So(stats, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})
		})
	})

	Convey("Given a container", t, func() {
		newContainer := &Container{
			ID: "container_id1", Names: []string{"/test_container1", "/test_container1_new"},
		}

		Convey("it can trim the / from its names", func() {
			newContainer.TrimNamePrefixes()
			So(newContainer.Names[0], ShouldEqual, "test_container1")
			So(newContainer.Names[1], ShouldEqual, "test_container1_new")
		})

		Convey("it can check if a given name is its valid name", func() {
			// call TrimNamePrefixes on the container
			newContainer.TrimNamePrefixes()

			Convey("for a correct name of the container", func() {
				So(newContainer.HasName("test_container1_new"), ShouldBeTrue)
			})

			Convey("but not for a wrong name of the container", func() {
				So(newContainer.HasName("wrong_name"), ShouldBeFalse)
			})
		})
	})
}
