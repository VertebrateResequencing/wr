// Copyright © 2020 Genome Research Limited
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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/inconshreveable/log15"
	. "github.com/smartystreets/goconvey/convey"
)

var testLogger = log15.New()

func TestConfig(t *testing.T) {
	fileTestSetup := func(dir, mport, mweb1, mweb2 string) (string, string, error) {
		path := filepath.Join(dir, ".wr_config.yml")
		_, err := os.Stat(path)
		if err == nil {
			return path, "", fmt.Errorf("%s already exists", path)
		}
		path2 := filepath.Join(dir, ".wr_config.development.yml")
		_, err = os.Stat(path2)
		if err == nil {
			return path, "", fmt.Errorf("%s already exists", path)
		}

		file, err := os.Create(path)
		if err != nil {
			return path, path2, err
		}
		file2, err := os.Create(path2)
		if err != nil {
			return path, path2, err
		}

		file.WriteString(fmt.Sprintf("managerport: \"%s\"\n", mport))
		file.WriteString(fmt.Sprintf("managerweb: \"%s\"\n", mweb1))
		file.Close()
		file2.WriteString(fmt.Sprintf("managerweb: \"%s\"\n", mweb2))
		file2.Close()

		return path, path2, nil
	}
	fileTestTeardown := func(path, path2 string) {
		err := os.Remove(path)
		if err != nil {
			fmt.Printf("\nfailed to delete %s: %s\n", path, err)
		}
		err = os.Remove(path2)
		if err != nil {
			fmt.Printf("\nfailed to delete %s: %s\n", path2, err)
		}
	}

	Convey("ConfigLoad gives default values to start with", t, func() {
		config := internal.ConfigLoad("development", false, testLogger)
		So(config.ManagerPort, ShouldNotBeBlank)
		So(config.Source("ManagerPort"), ShouldEqual, internal.ConfigSourceDefault)
		mweb := config.ManagerWeb
		So(mweb, ShouldNotBeBlank)
		So(config.Source("ManagerWeb"), ShouldEqual, internal.ConfigSourceDefault)
		So(config.ManagerUmask, ShouldEqual, 7)
		So(config.Source("ManagerUmask"), ShouldEqual, internal.ConfigSourceDefault)
		So(config.ManagerSetDomainIP, ShouldBeFalse)
		So(config.Source("ManagerSetDomainIP"), ShouldEqual, internal.ConfigSourceDefault)

		Convey("These can be overridden with Env Vars", func() {
			os.Setenv("WR_MANAGERPORT", "1234")
			os.Setenv("WR_MANAGERUMASK", "077")
			os.Setenv("WR_MANAGERSETDOMAINIP", "true")
			defer func() {
				os.Unsetenv("WR_MANAGERPORT")
				os.Unsetenv("WR_MANAGERUMASK")
				os.Unsetenv("WR_MANAGERSETDOMAINIP")
			}()
			config = internal.ConfigLoad("development", false, testLogger)
			So(config.ManagerPort, ShouldEqual, "1234")
			So(config.Source("ManagerPort"), ShouldEqual, internal.ConfigSourceEnvVar)
			So(config.ManagerWeb, ShouldEqual, mweb)
			So(config.Source("ManagerWeb"), ShouldEqual, internal.ConfigSourceDefault)
			So(config.ManagerUmask, ShouldEqual, 77)
			So(config.Source("ManagerUmask"), ShouldEqual, internal.ConfigSourceEnvVar)
			So(config.ManagerSetDomainIP, ShouldBeTrue)
			So(config.Source("ManagerSetDomainIP"), ShouldEqual, internal.ConfigSourceEnvVar)
		})

		Convey("These can be overridden with config files in WR_CONFIG_DIR", func() {
			dir, err := os.MkdirTemp("", "wr_conf_test")
			So(err, ShouldBeNil)
			defer os.RemoveAll(dir)

			mport := "1234"
			mweb1 := "1235"
			mweb2 := "1236"
			path, path2, err := fileTestSetup(dir, mport, mweb1, mweb2)
			defer fileTestTeardown(path, path2)
			So(err, ShouldBeNil)

			os.Setenv("WR_CONFIG_DIR", dir)
			defer func() {
				os.Unsetenv("WR_CONFIG_DIR")
			}()

			config = internal.ConfigLoad("development", false, testLogger)
			So(config.ManagerPort, ShouldEqual, mport)
			So(config.Source("ManagerPort"), ShouldEqual, path)
			So(config.ManagerWeb, ShouldEqual, mweb2)
			So(config.Source("ManagerWeb"), ShouldEqual, path2)

			Convey("These can be overridden with config files in home dir", func() {
				realHome, err := os.UserHomeDir()
				So(err, ShouldBeNil)
				newHome, err := os.MkdirTemp(dir, "home")
				So(err, ShouldBeNil)
				os.Setenv("HOME", newHome)
				defer func() {
					os.Setenv("HOME", realHome)
				}()
				home, err := os.UserHomeDir()
				So(err, ShouldBeNil)
				So(home, ShouldNotEqual, realHome)

				mport = "1334"
				mweb1 = "1335"
				mweb2 = "1336"
				path3, path4, err := fileTestSetup(home, mport, mweb1, mweb2)
				defer fileTestTeardown(path3, path4)
				So(err, ShouldBeNil)

				config = internal.ConfigLoad("development", false, testLogger)
				So(config.ManagerPort, ShouldEqual, mport)
				So(config.Source("ManagerPort"), ShouldEqual, path3)
				So(config.ManagerWeb, ShouldEqual, mweb2)
				So(config.Source("ManagerWeb"), ShouldEqual, path4)

				Convey("These can be overridden with config files in current dir", func() {
					pwd, err := os.Getwd()
					So(err, ShouldBeNil)
					mport = "1434"
					mweb1 = "1435"
					mweb2 = "1436"
					path5, path6, err := fileTestSetup(pwd, mport, mweb1, mweb2)
					defer fileTestTeardown(path5, path6)
					So(err, ShouldBeNil)

					config = internal.ConfigLoad("development", false, testLogger)
					So(config.ManagerPort, ShouldEqual, mport)
					So(config.Source("ManagerPort"), ShouldEqual, path5)
					So(config.ManagerWeb, ShouldEqual, mweb2)
					So(config.Source("ManagerWeb"), ShouldEqual, path6)
				})
			})
		})

		Convey("Tilda in ManagerDir is converted to abs path", func() {
			home, err := os.UserHomeDir()
			So(err, ShouldBeNil)
			os.Setenv("WR_MANAGERDIR", "~/foo")
			defer func() {
				os.Unsetenv("WR_MANAGERDIR")
			}()

			config = internal.ConfigLoad("development", false, testLogger)
			So(config.ManagerDir, ShouldEqual, filepath.Join(home, "foo_development"))

			Convey("Relative paths are converted to paths inside ManagerDir", func() {
				os.Setenv("WR_MANAGERPIDFILE", "bar")
				defer func() {
					os.Unsetenv("WR_MANAGERPIDFILE")
				}()

				config = internal.ConfigLoad("development", false, testLogger)
				So(config.ManagerPidFile, ShouldEqual, filepath.Join(home, "foo_development", "bar"))
			})
		})
	})
}
