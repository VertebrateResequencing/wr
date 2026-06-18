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
	"errors"
	"log"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// fileMode is the mode of the temp file created for testing.
const fileMode os.FileMode = 0600

// MockInteractor represents a mock implementation of container.Interactor.
type MockInteractor struct {
	ContainerListFn       func() ([]*Container, error)
	ContainerListInvoked  int
	ContainerStatsFn      func(string) (*Stats, error)
	ContainerStatsInvoked int
	ContainerKillFn       func(string) error
	ContainerKillInvoked  int
}

// ContainerList is a mock function which returns the list of containers.
func (m *MockInteractor) ContainerList(ctx context.Context) ([]*Container, error) {
	m.ContainerListInvoked++

	return m.ContainerListFn()
}

// ContainerStats is a mock function which returns the mem and cpu stats of a
// container given its ID.
func (m *MockInteractor) ContainerStats(ctx context.Context,
	containerID string) (*Stats, error) {
	m.ContainerStatsInvoked++

	return m.ContainerStatsFn(containerID)
}

// ContainerKill is a mock function which kills (removes the entry of) a
// container given its ID.
func (m *MockInteractor) ContainerKill(ctx context.Context, containerID string) error {
	m.ContainerKillInvoked++

	return m.ContainerKillFn(containerID)
}

// removeContainerEntry removes the container from mock container list if found and
// returns the remaining containers.
func removeContainerEntry(containerList []*Container, containerID string) []*Container {
	var remainingContainers []*Container

	for _, container := range containerList {
		if container.ID != containerID {
			remainingContainers = append(remainingContainers, container)
		}
	}

	return remainingContainers
}

// trimNamePrefixContainers trims the '/' character from the names of each container.
func trimNamePrefixContainers(cntrList []*Container) []*Container {
	for _, cntr := range cntrList {
		cntr.TrimNamePrefixes()
	}

	return cntrList
}

func TestOperator(t *testing.T) {
	ctx := context.Background()

	Convey("Given a NewOperator", t, func() {
		// Create a list of dummy containers
		cntrList := []*Container{{
			ID: "container_id1", Names: []string{"/test_container1", "/test_container1_2"},
		}, {
			ID: "container_id2", Names: []string{"/test_container2"},
		}, {
			ID: "container_id3", Names: []string{"/test_container3"},
		}}

		// Create a client with list of dummy containers
		newOperator := NewOperator(&MockInteractor{
			ContainerListFn: func() ([]*Container, error) {
				return trimNamePrefixContainers(cntrList), nil
			},

			ContainerKillFn: func(containerID string) error {
				remainContainers := removeContainerEntry(cntrList, containerID)
				if len(cntrList) == len(remainContainers) {
					return &OperatorError{Type: ErrContainerKill}
				}

				// Copy the remaining containers to cntrList
				cntrList = remainContainers

				return nil
			},
		},
		)

		// Create a client with no containers
		empNewOperator := NewOperator(&MockInteractor{
			ContainerListFn: func() ([]*Container, error) {
				return nil, &OperatorError{Type: ErrContainerList}
			},
		},
		)

		Convey("it can add the clients to the containers for stats", func() {
			So(cntrList[0].client, ShouldBeNil)
			newOperator.addClientToContainers(cntrList)

			So(cntrList[0].client, ShouldNotBeNil)
		})

		Convey("it can get the list of containers if exists", func() {
			clist, err := newOperator.GetCurrentContainers(ctx)
			So(err, ShouldBeNil)
			So(len(clist), ShouldEqual, 3)

			emplist, err := empNewOperator.GetCurrentContainers(ctx)
			So(err, ShouldNotBeNil)
			So(len(emplist), ShouldEqual, 0)
		})

		// Mark container_id3 to true in exisiting container, making it an "old"
		// container
		newOperator.existingContainers["container_id3"] = true

		Convey("it can remember the current container IDs", func() {
			Convey("when the list of containers is non-empty", func() {
				err := newOperator.RememberCurrentContainers(ctx)
				So(err, ShouldBeNil)
				So(newOperator.existingContainers["container_id1"], ShouldBeTrue)
				So(newOperator.existingContainers["container_id2"], ShouldBeTrue)
				So(newOperator.existingContainers["container_id3"], ShouldBeTrue)

				Convey("and check for an unknown container in that list", func() {
					So(newOperator.existingContainers["container_id4"], ShouldBeFalse)
				})

				Convey("and check for a newly created container", func() {
					newCntr := &Container{
						ID: "container_id4", Names: []string{"/test_container4", "/test_container4_new"},
					}
					newCntr.TrimNamePrefixes()
					cntrList = append(cntrList, newCntr)

					clist, err := newOperator.GetCurrentContainers(ctx)
					So(err, ShouldBeNil)
					So(len(clist), ShouldEqual, 4)

					So(newOperator.existingContainers["container_id4"], ShouldBeFalse)

					err = newOperator.RememberCurrentContainers(ctx)
					So(err, ShouldBeNil)
					So(newOperator.existingContainers["container_id4"], ShouldBeTrue)
				})
			})

			Convey("not when the list of containers is empty", func() {
				err := empNewOperator.RememberCurrentContainers(ctx)
				So(err, ShouldNotBeNil)
			})
		})

		Convey("it can get the list of new containers", func() {
			Convey("when the list is non-empty", func() {
				cntList, err := newOperator.GetNewContainers(ctx)
				So(err, ShouldBeNil)
				So(len(cntList), ShouldEqual, 2)

				err = newOperator.RememberCurrentContainers(ctx)
				So(err, ShouldBeNil)

				cntList, err = newOperator.GetNewContainers(ctx)
				So(err, ShouldBeNil)
				So(len(cntList), ShouldEqual, 0)
			})

			Convey("not when the list is empty", func() {
				cntList, errc := empNewOperator.GetNewContainers(ctx)
				So(errc, ShouldNotBeNil)
				So(len(cntList), ShouldEqual, 0)
			})
		})

		Convey("it can get the new container given a name", func() {
			Convey("when the list of containers is non-empty", func() {
				Convey("and the new container name is given", func() {
					cntr, err := newOperator.GetNewContainerByName(ctx, "test_container2")
					So(cntr, ShouldNotBeNil)
					So(err, ShouldBeNil)
				})

				Convey("but not when an old container name is given", func() {
					cntr, err := newOperator.GetNewContainerByName(ctx, "test_container3")
					So(cntr, ShouldBeNil)
					So(err, ShouldBeNil)
				})

				Convey("but not when an non-existing container name is given", func() {
					cntr, err := newOperator.GetNewContainerByName(ctx, "wrong_name")
					So(cntr, ShouldBeNil)
					So(err, ShouldBeNil)
				})
			})

			Convey("not when the list of containers is empty", func() {
				cntr, err := empNewOperator.GetNewContainerByName(ctx, "wrong_name")
				So(cntr, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})
		})

		Convey("it can get a container given the id", func() {
			Convey("for a client with a non-empty list of containers", func() {
				Convey("when the container id is correct", func() {
					cntr, err := newOperator.GetContainerByID(ctx, "container_id1")
					So(cntr, ShouldNotBeNil)
					So(err, ShouldBeNil)
				})

				Convey("when the container id is wrong", func() {
					cntr, err := newOperator.GetContainerByID(ctx, "wrong_id")
					So(cntr, ShouldBeNil)
					So(err, ShouldBeNil)
				})
			})

			Convey("but not for a client with an empty list of containers", func() {
				cntr, err := empNewOperator.GetContainerByID(ctx, "wrong_id")
				So(cntr, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})
		})

		Convey("and given a file path/glob path, return the container id", func() {
			// Create some files containing container id
			containerTempDir, err := os.MkdirTemp("", "container_temp_")
			if err != nil {
				log.Fatal(err)
			}

			defer os.RemoveAll(containerTempDir)

			containerFile := filepath.Join(containerTempDir, "Container.txt")
			err = os.WriteFile(containerFile, []byte("container_id2"), fileMode)
			So(err, ShouldBeNil)

			newContainerFile := filepath.Join(containerTempDir, "NewContainer.txt")
			err = os.WriteFile(newContainerFile, []byte("container_id4"), fileMode)
			So(err, ShouldBeNil)

			wrongContainerFile := filepath.Join(containerTempDir, "WrongContainer.txt")
			err = os.WriteFile(wrongContainerFile, []byte("container_id5"), fileMode)
			So(err, ShouldBeNil)

			containerEmptyFile := filepath.Join(containerTempDir, "containerEmpty.txt")
			err = os.WriteFile(containerEmptyFile, []byte(""), fileMode)
			So(err, ShouldBeNil)

			Convey("When the file path", func() {
				Convey("doesn't exist", func() {
					containerNonExistingFile := filepath.Join(containerTempDir, "containerNonExisting.txt")
					cntr, err := newOperator.cidPathToContainer(ctx, containerNonExistingFile)
					So(cntr, ShouldBeNil)
					So(err, ShouldNotBeNil)
				})

				Convey("has an empty file", func() {
					cntr, err := newOperator.cidPathToContainer(ctx, containerEmptyFile)
					So(cntr, ShouldBeNil)
					So(err, ShouldBeNil)
				})

				Convey("contains a file with correct container id", func() {
					cntr, err := newOperator.cidPathToContainer(ctx, containerFile)
					So(cntr, ShouldNotBeNil)
					So(err, ShouldBeNil)
				})

				Convey("contains a file with wrong container id", func() {
					cntr, err := newOperator.cidPathToContainer(ctx, wrongContainerFile)
					So(cntr, ShouldBeNil)
					So(err, ShouldBeNil)
				})
			})

			Convey("When the file path is a glob and", func() {
				Convey("the path pattern is correct", func() {
					cntr, err := newOperator.cidPathGlobToContainer(ctx, containerTempDir+"/*")
					So(cntr, ShouldNotBeNil)
					So(err, ShouldBeNil)
				})

				Convey("the path doesn't exist", func() {
					cntr, err := newOperator.cidPathGlobToContainer(ctx, "/randomContainerPath/*")
					So(cntr, ShouldBeNil)
					So(err, ShouldBeNil)
				})

				Convey("path pattern is wrong", func() {
					cntr, err := newOperator.cidPathGlobToContainer(ctx, "[")
					So(cntr, ShouldBeNil)
					So(err, ShouldNotBeNil)
				})

				Convey("there is no file containing the container id in the path", func() {
					cntr, err := newOperator.cidPathGlobToContainer(ctx, containerTempDir+"/container*")
					So(cntr, ShouldBeNil)
					So(err, ShouldBeNil)
				})

				Convey("not for a client with a empty list of containers", func() {
					cntr, err := empNewOperator.cidPathGlobToContainer(ctx, containerTempDir+"/*")
					So(cntr, ShouldBeNil)
					So(err, ShouldBeNil)
				})
			})

			Convey("Given a file path/glob file path and return a valid container id", func() {
				Convey("For a correct file path", func() {
					cntr, err := newOperator.GetContainerByPath(ctx, "Container.txt", containerTempDir)
					So(cntr, ShouldNotBeNil)
					So(err, ShouldBeNil)
				})

				Convey("For a correct glob path", func() {
					cntr, err := newOperator.GetContainerByPath(ctx, containerTempDir+"/*", "")
					So(cntr, ShouldNotBeNil)
					So(err, ShouldBeNil)
				})
			})
		})

		Convey("and a container id, it can kill a container", func() {
			Convey("when the container exists", func() {
				err := newOperator.KillContainer(ctx, "container_id1")
				So(err, ShouldBeNil)

				clist, err := newOperator.GetCurrentContainers(ctx)
				So(len(clist), ShouldEqual, 2)
				So(err, ShouldBeNil)
			})

			Convey("not when the container doesn't exist", func() {
				err := newOperator.KillContainer(ctx, "container_id5")
				So(err, ShouldNotBeNil)

				clist, err := newOperator.GetCurrentContainers(ctx)
				So(len(clist), ShouldEqual, 3)
				So(err, ShouldBeNil)
			})
		})
	})

	Convey("Unwrap the error", t, func() {
		// Create a client with no containers and make it return an error on
		// container listing
		empOperator := NewOperator(&MockInteractor{
			ContainerListFn: func() ([]*Container, error) {
				_, err := os.ReadFile("nonExistingFile.txt")

				return nil, err
			},
		},
		)

		Convey("when the list of containers doesn't exist", func() {
			emplist, err := empOperator.GetCurrentContainers(ctx)
			So(len(emplist), ShouldEqual, 0)

			var e *OperatorError
			So(errors.As(err, &e), ShouldBeTrue)
			So(e.Unwrap(), ShouldNotBeNil)
		})
	})
}
