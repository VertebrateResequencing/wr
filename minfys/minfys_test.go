// Copyright © 2016-2017 Genome Research Limited
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

package minfys

import (
	"bufio"
	"fmt"
	. "github.com/smartystreets/goconvey/convey"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const crfile = "cloud.resources"

func TestMinFys(t *testing.T) {
	target := os.Getenv("WR_S3_TARGET")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	// For these tests to work, $WR_S3_TARGET must be the full URL to an
	// immediate child directory of a bucket that you have read and write
	// permissions for, eg: https://cog.domain.com/bucket/wr_tests
	// You must also have a ~/.s3cfg file with a [default] section specifying
	// the same domain and scheme via host_base and use_https.
	//
	// The child directory must contain the following:
	// perl -e 'for (1..100000) { printf "%06d\n", $_; }' > 100k.lines
	// echo 1234567890abcdefghijklmnopqrstuvwxyz1234567890 > numalphanum.txt
	// dd if=/dev/zero of=1G.file bs=1073741824 count=1
	// mkdir -p sub/deep
	// touch sub/empty.file
	// echo foo > sub/deep/bar
	// export WR_BUCKET_SUB=s3://bucket/wr_tests
	// s3cmd put 100k.lines $WR_BUCKET_SUB/100k.lines
	// s3cmd put numalphanum.txt $WR_BUCKET_SUB/numalphanum.txt
	// s3cmd put 1G.file $WR_BUCKET_SUB/1G.file
	// s3cmd put sub/empty.file $WR_BUCKET_SUB/sub/empty.file
	// s3cmd put sub/deep/bar $WR_BUCKET_SUB/sub/deep/bar
	// rm -fr 100k.lines numalphanum.txt 1G.file sub
	// [use s3fs to mkdir s3://bucket/wr_tests/emptyDir]

	if target == "" || accessKey == "" || secretKey == "" {
		SkipConvey("Without WR_S3_TARGET, AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables, we'll skip minfys tests", t, func() {})
	} else {
		crdir, err := ioutil.TempDir("", "wr_testing_minfys")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(crdir)
		mountPoint := filepath.Join(crdir, "mount")
		cacheDir := filepath.Join(crdir, "cacheDir")

		targetManual := &Target{
			Target:    target,
			AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			CacheData: true,
			Write:     false,
		}

		cfg := &Config{
			Mount:   mountPoint,
			Retries: 3,
			Verbose: false,
			Quiet:   false,
			Targets: []*Target{targetManual},
		}

		Convey("You can configure targets from the environment", t, func() {
			targetEnv := &Target{}
			err = targetEnv.ReadEnvironment("", "mybucket/subdir")
			So(err, ShouldBeNil)
			So(targetEnv.AccessKey, ShouldEqual, targetManual.AccessKey)
			So(targetEnv.SecretKey, ShouldEqual, targetManual.SecretKey)
			So(targetEnv.Target, ShouldNotBeNil)
			u, _ := url.Parse(target)
			uNew := url.URL{
				Scheme: u.Scheme,
				Host:   u.Host,
				Path:   "mybucket/subdir",
			}
			So(targetEnv.Target, ShouldEqual, uNew.String())

			targetEnv2 := &Target{}
			err = targetEnv2.ReadEnvironment("default", "mybucket/subdir")
			So(err, ShouldBeNil)
			So(targetEnv2.AccessKey, ShouldEqual, targetEnv.AccessKey)
			So(targetEnv2.SecretKey, ShouldEqual, targetEnv.SecretKey)
			So(targetEnv2.Target, ShouldEqual, targetEnv.Target)

			targetEnv3 := &Target{}
			err = targetEnv3.ReadEnvironment("-fake-", "mybucket/subdir")
			So(err, ShouldNotBeNil)

			// *** how can we test chaining of ~/.s3cfg and ~/.aws/credentials
			// without messing with those files?
		})

		// *** don't know how to test UnmountOnDeath()...

		var bigFileGetTime time.Duration
		Convey("You can mount with local file caching", t, func() {
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount()
			So(err, ShouldBeNil)

			defer func() {
				err = fs.Unmount()
				So(err, ShouldBeNil)
			}()

			Convey("You can read a whole file as well as parts of it by seeking", func() {
				path := mountPoint + "/100k.lines"
				read, err := streamFile(path, 0)
				So(err, ShouldBeNil)
				So(read, ShouldEqual, 700000)

				read, err = streamFile(path, 350000)
				So(err, ShouldBeNil)
				So(read, ShouldEqual, 350000)
			})

			Convey("You can do random reads", func() {
				// it works on a small file
				path := mountPoint + "/numalphanum.txt"
				r, err := os.Open(path)
				So(err, ShouldBeNil)
				defer r.Close()

				r.Seek(36, io.SeekStart)

				b := make([]byte, 10, 10)
				done, err := io.ReadFull(r, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 10)
				So(b, ShouldResemble, []byte("1234567890"))

				r.Seek(10, io.SeekStart)
				b = make([]byte, 10, 10)
				done, err = io.ReadFull(r, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 10)
				So(b, ShouldResemble, []byte("abcdefghij"))

				// and it works on a big file
				path = mountPoint + "/100k.lines"
				rbig, err := os.Open(path)
				So(err, ShouldBeNil)
				defer rbig.Close()

				rbig.Seek(350000, io.SeekStart)
				b = make([]byte, 6, 6)
				done, err = io.ReadFull(rbig, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 6)
				So(b, ShouldResemble, []byte("050001"))

				rbig.Seek(175000, io.SeekStart)
				b = make([]byte, 6, 6)
				done, err = io.ReadFull(rbig, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 6)
				So(b, ShouldResemble, []byte("025001"))
			})

			Convey("You can read a very big file", func() {
				path := mountPoint + "/1G.file"
				start := time.Now()
				read, err := streamFile(path, 0)
				bigFileGetTime = time.Since(start)
				So(err, ShouldBeNil)
				So(read, ShouldEqual, 1073741824)
			})

			Convey("Trying to write in non Write mode fails", func() {
				path := mountPoint + "/write.test"
				b := []byte("write test\n")
				err := ioutil.WriteFile(path, b, 0644)
				So(err, ShouldNotBeNil)
				perr, ok := err.(*os.PathError)
				So(ok, ShouldBeTrue)
				So(perr.Error(), ShouldContainSubstring, "operation not permitted")
			})

			Convey("You can't delete files either", func() {
				path := mountPoint + "/1G.file"
				err = os.Remove(path)
				So(err, ShouldNotBeNil)
				perr, ok := err.(*os.PathError)
				So(ok, ShouldBeTrue)
				So(perr.Error(), ShouldContainSubstring, "operation not permitted")
			})

			Convey("And you can't rename files", func() {
				path := mountPoint + "/1G.file"
				dest := mountPoint + "/1G.moved"
				cmd := exec.Command("mv", path, dest)
				err = cmd.Run()
				So(err, ShouldNotBeNil)
			})

			Convey("You can't touch files in non Write mode", func() {
				path := mountPoint + "/1G.file"
				cmd := exec.Command("touch", path)
				err = cmd.Run()
				So(err, ShouldNotBeNil)
			})

			Convey("You can't make, delete or rename directories in non Write mode", func() {
				newDir := mountPoint + "/newdir_test"
				cmd := exec.Command("mkdir", newDir)
				err = cmd.Run()
				So(err, ShouldNotBeNil)

				path := mountPoint + "/sub"
				cmd = exec.Command("rmdir", path)
				err = cmd.Run()
				So(err, ShouldNotBeNil)

				cmd = exec.Command("mv", path, newDir)
				err = cmd.Run()
				So(err, ShouldNotBeNil)
			})
		})

		Convey("You can mount with local file caching in write mode", t, func() {
			targetManual.Write = true
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount()
			So(err, ShouldBeNil)

			defer func() {
				err = fs.Unmount()
				targetManual.Write = false
				So(err, ShouldBeNil)
			}()

			Convey("Trying to write in write mode works", func() {
				path := mountPoint + "/write.test"
				b := []byte("write test\n")
				err := ioutil.WriteFile(path, b, 0644)
				So(err, ShouldBeNil)

				// you can immediately read it back
				bytes, err := ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)

				// (because it's in the the local cache)
				cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
				_, err = os.Stat(cachePath)
				So(err, ShouldBeNil)

				// and it's statable and listable
				_, err = os.Stat(path)
				So(err, ShouldBeNil)

				entries, err := ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)
				details := dirDetails(entries)
				rootEntries := []string{"100k.lines:file:700000", "1G.file:file:1073741824", "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test:file:11"}
				So(details, ShouldResemble, rootEntries)

				// unmounting causes the local cached file to be deleted
				err = fs.Unmount()
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)
				_, err = os.Stat(path)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				// remounting lets us read the file again - it actually got
				// uploaded
				err = fs.Mount()
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				bytes, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)

				_, err = os.Stat(cachePath)
				So(err, ShouldBeNil)

				Convey("You can append to a cached file", func() {
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
					So(err, ShouldBeNil)

					line2 := "line2\n"
					_, err = f.WriteString(line2)
					f.Close()
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, string(b)+line2)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					_, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount()
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, string(b)+line2)

					Convey("You can truncate a cached file", func() {
						err := os.Truncate(path, 0)
						So(err, ShouldBeNil)

						cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
						stat, err := os.Stat(cachePath)
						So(err, ShouldBeNil)
						So(stat.Size(), ShouldEqual, 0)
						stat, err = os.Stat(path)
						So(err, ShouldBeNil)
						So(stat.Size(), ShouldEqual, 0)

						err = fs.Unmount()
						So(err, ShouldBeNil)

						stat, err = os.Stat(cachePath)
						So(err, ShouldNotBeNil)
						So(os.IsNotExist(err), ShouldBeTrue)

						err = fs.Mount()
						So(err, ShouldBeNil)

						stat, err = os.Stat(cachePath)
						So(err, ShouldNotBeNil)
						So(os.IsNotExist(err), ShouldBeTrue)
						stat, err = os.Stat(path)
						So(err, ShouldBeNil)
						So(stat.Size(), ShouldEqual, 0)
						bytes, err = ioutil.ReadFile(path)
						So(err, ShouldBeNil)
						So(string(bytes), ShouldEqual, "")

						Convey("You can delete files", func() {
							err = os.Remove(path)
							So(err, ShouldBeNil)

							_, err = os.Stat(cachePath)
							So(err, ShouldNotBeNil)
							So(os.IsNotExist(err), ShouldBeTrue)
							_, err = os.Stat(path)
							So(err, ShouldNotBeNil)
							So(os.IsNotExist(err), ShouldBeTrue)
						})
					})

					Convey("You can truncate a cached file using an offset", func() {
						err := os.Truncate(path, 3)
						So(err, ShouldBeNil)

						cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
						stat, err := os.Stat(cachePath)
						So(err, ShouldBeNil)
						So(stat.Size(), ShouldEqual, 3)
						stat, err = os.Stat(path)
						So(err, ShouldBeNil)
						So(stat.Size(), ShouldEqual, 3)

						err = fs.Unmount()
						So(err, ShouldBeNil)

						stat, err = os.Stat(cachePath)
						So(err, ShouldNotBeNil)
						So(os.IsNotExist(err), ShouldBeTrue)

						err = fs.Mount()
						So(err, ShouldBeNil)

						stat, err = os.Stat(cachePath)
						So(err, ShouldNotBeNil)
						So(os.IsNotExist(err), ShouldBeTrue)
						stat, err = os.Stat(path)
						So(err, ShouldBeNil)
						So(stat.Size(), ShouldEqual, 3)
						bytes, err := ioutil.ReadFile(path)
						So(err, ShouldBeNil)
						So(string(bytes), ShouldEqual, "wri")

						err = os.Remove(path)
						So(err, ShouldBeNil)
					})

					Convey("You can truncate a cached file and then write to it", func() {
						err := os.Truncate(path, 0)
						So(err, ShouldBeNil)

						f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
						So(err, ShouldBeNil)

						line := "trunc\n"
						_, err = f.WriteString(line)
						f.Close()
						So(err, ShouldBeNil)

						bytes, err := ioutil.ReadFile(path)
						So(err, ShouldBeNil)
						So(string(bytes), ShouldEqual, line)

						err = fs.Unmount()
						So(err, ShouldBeNil)

						cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
						_, err = os.Stat(cachePath)
						So(err, ShouldNotBeNil)
						So(os.IsNotExist(err), ShouldBeTrue)
						_, err = os.Stat(path)
						So(err, ShouldNotBeNil)
						So(os.IsNotExist(err), ShouldBeTrue)

						err = fs.Mount()
						So(err, ShouldBeNil)

						bytes, err = ioutil.ReadFile(path)
						So(err, ShouldBeNil)
						So(string(bytes), ShouldEqual, line)

						err = os.Remove(path)
						So(err, ShouldBeNil)
					})
				})

				Convey("You can rename files using mv", func() {
					dest := mountPoint + "/write.moved"
					cmd := exec.Command("mv", path, dest)
					err = cmd.Run()
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(dest)
					So(err, ShouldBeNil)
					So(bytes, ShouldResemble, b)

					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)

					err = fs.Unmount()
					So(err, ShouldBeNil)
					err = fs.Mount()
					So(err, ShouldBeNil)

					defer func() {
						err = os.Remove(dest)
						So(err, ShouldBeNil)
					}()

					bytes, err = ioutil.ReadFile(dest)
					So(err, ShouldBeNil)
					So(bytes, ShouldResemble, b)

					_, err = os.Stat(dest)
					So(err, ShouldBeNil)

					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)
				})

				Convey("You can rename uncached files using os.Rename", func() {
					// unmount first to clear the cache
					err = fs.Unmount()
					So(err, ShouldBeNil)
					err = fs.Mount()
					So(err, ShouldBeNil)

					dest := mountPoint + "/write.moved"
					err := os.Rename(path, dest)
					So(err, ShouldBeNil)

					_, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					cachePathDest := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.moved"))
					_, err = os.Stat(cachePathDest)
					So(err, ShouldNotBeNil)

					bytes, err = ioutil.ReadFile(dest)
					So(err, ShouldBeNil)
					So(bytes, ShouldResemble, b)

					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)

					err = fs.Unmount()
					So(err, ShouldBeNil)
					err = fs.Mount()
					So(err, ShouldBeNil)

					defer func() {
						err = os.Remove(dest)
						So(err, ShouldBeNil)
					}()

					bytes, err = ioutil.ReadFile(dest)
					So(err, ShouldBeNil)
					So(bytes, ShouldResemble, b)

					_, err = os.Stat(dest)
					So(err, ShouldBeNil)

					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)
				})

				Convey("You can rename cached and altered files", func() {
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
					So(err, ShouldBeNil)

					line2 := "line2\n"
					_, err = f.WriteString(line2)
					f.Close()
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, string(b)+line2)

					dest := mountPoint + "/write.moved"
					err = os.Rename(path, dest)
					So(err, ShouldBeNil)

					_, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					cachePathDest := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.moved"))
					_, err = os.Stat(cachePathDest)
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(dest)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, string(b)+line2)

					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)

					err = fs.Unmount()
					So(err, ShouldBeNil)
					err = fs.Mount()
					So(err, ShouldBeNil)

					defer func() {
						err = os.Remove(dest)
						So(err, ShouldBeNil)
					}()

					bytes, err = ioutil.ReadFile(dest)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, string(b)+line2)

					_, err = os.Stat(dest)
					So(err, ShouldBeNil)

					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)
				})
			})

			Convey("You can't rename remote directories", func() {
				newDir := mountPoint + "/newdir_test"
				subDir := mountPoint + "/sub"
				cmd := exec.Command("mv", subDir, newDir)
				err = cmd.Run()
				So(err, ShouldNotBeNil)
			})

			Convey("You can't remove remote directories", func() {
				subDir := mountPoint + "/sub"
				cmd := exec.Command("rmdir", subDir)
				err = cmd.Run()
				So(err, ShouldNotBeNil)
			})

			Convey("You can create directories and rename and remove those", func() {
				newDir := mountPoint + "/newdir_test"
				cmd := exec.Command("mkdir", newDir)
				err = cmd.Run()
				So(err, ShouldBeNil)

				entries, err := ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)
				details := dirDetails(entries)
				rootEntries := []string{"100k.lines:file:700000", "1G.file:file:1073741824", "emptyDir:dir", "newdir_test:dir", "numalphanum.txt:file:47", "sub:dir"}
				So(details, ShouldResemble, rootEntries)

				movedDir := mountPoint + "/newdir_moved"
				cmd = exec.Command("mv", newDir, movedDir)
				err = cmd.Run()
				So(err, ShouldBeNil)

				entries, err = ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)
				details = dirDetails(entries)
				rootEntries = []string{"100k.lines:file:700000", "1G.file:file:1073741824", "emptyDir:dir", "newdir_moved:dir", "numalphanum.txt:file:47", "sub:dir"}
				So(details, ShouldResemble, rootEntries)

				cmd = exec.Command("rmdir", movedDir)
				err = cmd.Run()
				So(err, ShouldBeNil)

				Convey("You can create nested directories and add files to them", func() {
					nestedDir := mountPoint + "/newdir_test/a/b/c"
					err = os.MkdirAll(nestedDir, os.FileMode(700))
					So(err, ShouldBeNil)

					path := nestedDir + "/write.nested"
					b := []byte("nested test\n")
					err := ioutil.WriteFile(path, b, 0644)
					So(err, ShouldBeNil)

					bytes, err := ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(bytes, ShouldResemble, b)

					entries, err := ioutil.ReadDir(nestedDir)
					So(err, ShouldBeNil)
					details := dirDetails(entries)
					nestEntries := []string{"write.nested:file:12"}
					So(details, ShouldResemble, nestEntries)

					err = fs.Unmount()
					So(err, ShouldBeNil)
					err = fs.Mount()
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(bytes, ShouldResemble, b)

					os.Remove(path)
				})
			})

			Convey("In write mode, you can create a file to test with...", func() {
				// create a file we can play with first
				path := mountPoint + "/write.test"
				b := []byte("write test\n")
				err := ioutil.WriteFile(path, b, 0644)
				So(err, ShouldBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(path)
					So(err, ShouldBeNil)
				}()

				err = fs.Mount()
				So(err, ShouldBeNil)

				Convey("You can't write to a file you open RDONLY", func() {
					f, err := os.OpenFile(path, os.O_RDONLY, 0644)
					So(err, ShouldBeNil)
					_, err = f.WriteString("fails\n")
					f.Close()
					So(err, ShouldNotBeNil)
				})

				Convey("You can append to an uncached file", func() {
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
					So(err, ShouldBeNil)

					line2 := "line2\n"
					_, err = f.WriteString(line2)
					f.Close()
					So(err, ShouldBeNil)

					bytes, err := ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, string(b)+line2)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					_, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount()
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, string(b)+line2)
				})

				Convey("You can truncate an uncached file", func() {
					err := os.Truncate(path, 0)
					So(err, ShouldBeNil)

					cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					stat, err := os.Stat(cachePath)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 0)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 0)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					stat, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount()
					So(err, ShouldBeNil)

					stat, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 0)
					bytes, err := ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, "")
				})

				Convey("You can truncate an uncached file using an offset", func() {
					err := os.Truncate(path, 3)
					So(err, ShouldBeNil)

					cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					stat, err := os.Stat(cachePath)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 3)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 3)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					stat, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount()
					So(err, ShouldBeNil)

					stat, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 3)
					bytes, err := ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, "wri")
				})

				Convey("You can truncate an uncached file using an Open call", func() {
					f, err := os.OpenFile(path, os.O_TRUNC, 0644)
					So(err, ShouldBeNil)
					f.Close()

					cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					stat, err := os.Stat(cachePath)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 0)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 0)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					stat, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount()
					So(err, ShouldBeNil)

					stat, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 0)
					bytes, err := ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, "")
				})

				Convey("You can truncate an uncached file and immediately write to it", func() {
					err := os.Truncate(path, 0)
					So(err, ShouldBeNil)

					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
					So(err, ShouldBeNil)

					line := "trunc\n"
					_, err = f.WriteString(line)
					f.Close()
					So(err, ShouldBeNil)

					bytes, err := ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, line)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					_, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount()
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, line)
				})

				SkipConvey("You can truncate an uncached file using an Open call and write to it", func() {
					f, err := os.OpenFile(path, os.O_TRUNC|os.O_WRONLY, 0644)
					So(err, ShouldBeNil)
					//*** this fails because it results in an fs.Open() call
					// where I see the os.O_WRONLY flag but not the os.O_TRUNC
					// flag

					line := "trunc\n"
					_, err = f.WriteString(line)
					f.Close()
					So(err, ShouldBeNil)

					bytes, err := ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, line)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					_, err = os.Stat(cachePath)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount()
					So(err, ShouldBeNil)

					bytes, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, line)
				})

				Convey("You can write to the mount point and immediately delete the file and get the correct listing", func() {
					entries, err := ioutil.ReadDir(mountPoint)
					So(err, ShouldBeNil)
					details := dirDetails(entries)
					subEntries := []string{"100k.lines:file:700000", "1G.file:file:1073741824", "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test:file:11"}
					So(details, ShouldResemble, subEntries)

					path := mountPoint + "/write.test2"
					b := []byte("write test2\n")
					err = ioutil.WriteFile(path, b, 0644)
					So(err, ShouldBeNil)

					// it's statable and listable
					_, err = os.Stat(path)
					So(err, ShouldBeNil)

					entries, err = ioutil.ReadDir(mountPoint)
					So(err, ShouldBeNil)
					details = dirDetails(entries)
					subEntries = []string{"100k.lines:file:700000", "1G.file:file:1073741824", "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test2:file:12", "write.test:file:11"}
					So(details, ShouldResemble, subEntries)

					// once deleted, it's no longer listed
					err = os.Remove(path)
					So(err, ShouldBeNil)

					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)

					entries, err = ioutil.ReadDir(mountPoint)
					So(err, ShouldBeNil)
					details = dirDetails(entries)
					subEntries = []string{"100k.lines:file:700000", "1G.file:file:1073741824", "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test:file:11"}
					So(details, ShouldResemble, subEntries)

					// running unix ls reveals problems that ReadDir doesn't
					cmd := exec.Command("ls", "-alth", mountPoint)
					err = cmd.Run()
					So(err, ShouldBeNil)
				})

				Convey("You can touch an uncached file", func() {
					info, err := os.Stat(path)
					cmd := exec.Command("touch", "-d", "2006-01-02 15:04:05", path)
					err = cmd.Run()
					So(err, ShouldBeNil)
					info2, err := os.Stat(path)
					So(info.ModTime().Unix(), ShouldNotAlmostEqual, info2.ModTime().Unix(), 62)
					So(info2.ModTime().String(), ShouldEqual, "2006-01-02 15:04:05 +0000 UTC")
				})

				Convey("You can immediately touch an uncached file", func() {
					cmd := exec.Command("touch", "-d", "2006-01-02 15:04:05", path)
					err := cmd.Run()
					So(err, ShouldBeNil)

					// (looking at the contents of a subdir revealed a bug)
					entries, err := ioutil.ReadDir(mountPoint + "/sub")
					So(err, ShouldBeNil)
					details := dirDetails(entries)
					subEntries := []string{"deep:dir", "empty.file:file:0"}
					So(details, ShouldResemble, subEntries)

					info, err := os.Stat(path)
					So(info.ModTime().String(), ShouldEqual, "2006-01-02 15:04:05 +0000 UTC")
				})

				Convey("You can touch a cached file", func() {
					info, err := os.Stat(path)
					_, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)

					cmd := exec.Command("touch", "-d", "2006-01-02 15:04:05", path)
					err = cmd.Run()
					So(err, ShouldBeNil)
					info2, err := os.Stat(path)
					So(info.ModTime().Unix(), ShouldNotAlmostEqual, info2.ModTime().Unix(), 62)
					So(info2.ModTime().String(), ShouldEqual, "2006-01-02 15:04:05 +0000 UTC")

					cmd = exec.Command("touch", "-d", "2007-01-02 15:04:05", path)
					err = cmd.Run()
					So(err, ShouldBeNil)
					info3, err := os.Stat(path)
					So(info2.ModTime().Unix(), ShouldNotAlmostEqual, info3.ModTime().Unix(), 62)
					So(info3.ModTime().String(), ShouldEqual, "2007-01-02 15:04:05 +0000 UTC")
				})

				Convey("You can directly change the mtime on a cached file", func() {
					info, err := os.Stat(path)
					_, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)

					t := time.Now().Add(5 * time.Minute)
					err = os.Chtimes(path, t, t)
					So(err, ShouldBeNil)
					info2, err := os.Stat(path)
					So(info.ModTime().Unix(), ShouldNotAlmostEqual, info2.ModTime().Unix(), 62)
					So(info2.ModTime().Unix(), ShouldAlmostEqual, t.Unix(), 2)
				})

				Convey("But not an uncached one", func() {
					info, err := os.Stat(path)
					t := time.Now().Add(5 * time.Minute)
					err = os.Chtimes(path, t, t)
					So(err, ShouldBeNil)
					info2, err := os.Stat(path)
					So(info.ModTime().Unix(), ShouldAlmostEqual, info2.ModTime().Unix(), 62)
					So(info2.ModTime().Unix(), ShouldNotAlmostEqual, t.Unix(), 2)
				})
			})

			Convey("You can immediately write in to a subdirectory", func() {
				path := mountPoint + "/sub/write.test"
				b := []byte("write test\n")
				err := ioutil.WriteFile(path, b, 0644)
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(path)
					So(err, ShouldBeNil)
				}()

				// you can immediately read it back
				bytes, err := ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)

				// and it's statable and listable
				_, err = os.Stat(path)
				So(err, ShouldBeNil)

				entries, err := ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)
				details := dirDetails(entries)
				subEntries := []string{"deep:dir", "empty.file:file:0", "write.test:file:11"}
				So(details, ShouldResemble, subEntries)

				err = fs.Unmount()
				So(err, ShouldBeNil)

				// remounting lets us read the file again - it actually got
				// uploaded
				err = fs.Mount()
				So(err, ShouldBeNil)

				bytes, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)
			})

			Convey("You can write in to a subdirectory that has been previously listed", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)
				details := dirDetails(entries)
				subEntries := []string{"deep:dir", "empty.file:file:0"}
				So(details, ShouldResemble, subEntries)

				path := mountPoint + "/sub/write.test"
				b := []byte("write test\n")
				err = ioutil.WriteFile(path, b, 0644)
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(path)
					So(err, ShouldBeNil)
				}()

				// you can immediately read it back
				bytes, err := ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)

				// and it's statable and listable
				_, err = os.Stat(path)
				So(err, ShouldBeNil)

				entries, err = ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)
				details = dirDetails(entries)
				subEntries = []string{"deep:dir", "empty.file:file:0", "write.test:file:11"}
				So(details, ShouldResemble, subEntries)

				err = fs.Unmount()
				So(err, ShouldBeNil)

				// remounting lets us read the file again - it actually got
				// uploaded
				err = fs.Mount()
				So(err, ShouldBeNil)

				bytes, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)
			})

			Convey("You can write in to a subdirectory and immediately delete the file and get the correct listing", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)
				details := dirDetails(entries)
				subEntries := []string{"deep:dir", "empty.file:file:0"}
				So(details, ShouldResemble, subEntries)

				path := mountPoint + "/sub/write.test"
				b := []byte("write test\n")
				err = ioutil.WriteFile(path, b, 0644)
				So(err, ShouldBeNil)

				// it's statable and listable
				_, err = os.Stat(path)
				So(err, ShouldBeNil)

				entries, err = ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)
				details = dirDetails(entries)
				subEntries = []string{"deep:dir", "empty.file:file:0", "write.test:file:11"}
				So(details, ShouldResemble, subEntries)

				// once deleted, it's no longer listed
				err = os.Remove(path)
				So(err, ShouldBeNil)

				_, err = os.Stat(path)
				So(err, ShouldNotBeNil)

				entries, err = ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)
				details = dirDetails(entries)
				subEntries = []string{"deep:dir", "empty.file:file:0"}
				So(details, ShouldResemble, subEntries)

				// running unix ls reveals problems that ReadDir doesn't
				cmd := exec.Command("ls", "-alth", mountPoint+"/sub")
				err = cmd.Run()
				So(err, ShouldBeNil)
			})

			Convey("You can touch a non-existent file", func() {
				path := mountPoint + "/write.test"
				cmd := exec.Command("touch", path)
				err = cmd.Run()
				defer func() {
					err = os.Remove(path)
					So(err, ShouldBeNil)
				}()
				So(err, ShouldBeNil)
			})

			Convey("You can write multiple files and they get uploaded in final mtime order", func() {
				path1 := mountPoint + "/write.test1"
				b := []byte("write test1\n")
				err := ioutil.WriteFile(path1, b, 0644)
				So(err, ShouldBeNil)

				path2 := mountPoint + "/write.test2"
				b = []byte("write test2\n")
				err = ioutil.WriteFile(path2, b, 0644)
				So(err, ShouldBeNil)

				path3 := mountPoint + "/write.test3"
				b = []byte("write test3\n")
				err = ioutil.WriteFile(path3, b, 0644)
				So(err, ShouldBeNil)

				cmd := exec.Command("touch", "-d", "2006-01-02 15:04:05", path2)
				err = cmd.Run()
				So(err, ShouldBeNil)

				t := time.Now().Add(5 * time.Minute)
				err = os.Chtimes(path1, t, t)
				So(err, ShouldBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)

				defer func() {
					os.Remove(path1)
					os.Remove(path2)
					os.Remove(path3)
				}()

				err = fs.Mount()
				So(err, ShouldBeNil)

				info1, err := os.Stat(path1)
				So(err, ShouldBeNil)
				info2, err := os.Stat(path2)
				So(err, ShouldBeNil)
				info3, err := os.Stat(path3)
				So(err, ShouldBeNil)
				So(info2.ModTime().Unix(), ShouldBeLessThanOrEqualTo, info3.ModTime().Unix())
				So(info3.ModTime().Unix(), ShouldBeLessThanOrEqualTo, info1.ModTime().Unix())

				// *** unfortunately they only get second-resolution mtimes, and
				// they all get uploaded in the same second, so this isn't a very
				// good test... need uploads that take more than 1 second each...
			})
		})

		Convey("You can mount with local file caching in an explicit location", t, func() {
			targetManual.CacheDir = cacheDir
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount()
			So(err, ShouldBeNil)

			defer func() {
				err = fs.Unmount()
				targetManual.CacheDir = ""
				So(err, ShouldBeNil)
			}()

			path := mountPoint + "/numalphanum.txt"
			_, err = ioutil.ReadFile(path)
			So(err, ShouldBeNil)

			cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("numalphanum.txt"))
			_, err = os.Stat(cachePath)
			So(err, ShouldBeNil)
			So(cachePath, ShouldStartWith, cacheDir)

			Convey("Unmounting doesn't delete the cache", func() {
				err = fs.Unmount()
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
				So(err, ShouldBeNil)
			})
		})

		Convey("You can mount in write mode and not upload on unmount", t, func() {
			targetManual.Write = true
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount()
			So(err, ShouldBeNil)

			defer func() {
				err = fs.Unmount()
				targetManual.Write = false
				So(err, ShouldBeNil)
			}()

			path := mountPoint + "/write.test"
			b := []byte("write test\n")
			err = ioutil.WriteFile(path, b, 0644)
			So(err, ShouldBeNil)

			bytes, err := ioutil.ReadFile(path)
			So(err, ShouldBeNil)
			So(bytes, ShouldResemble, b)

			cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
			_, err = os.Stat(cachePath)
			So(err, ShouldBeNil)

			// unmount without uploads
			err = fs.Unmount(true)
			So(err, ShouldBeNil)

			_, err = os.Stat(cachePath)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
			_, err = os.Stat(path)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)

			// remounting reveals it did not get uploaded
			err = fs.Mount()
			So(err, ShouldBeNil)

			_, err = os.Stat(cachePath)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)

			_, err = os.Stat(path)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
		})

		Convey("You can mount with verbose yet quiet, and still get the logs", t, func() {
			origVerbose := cfg.Verbose
			cfg.Verbose = true
			cfg.Quiet = true
			defer func() {
				cfg.Verbose = origVerbose
				cfg.Quiet = false
			}()
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount()
			So(err, ShouldBeNil)

			_, err = ioutil.ReadDir(mountPoint)
			expectedErrorLog := ""
			if err != nil {
				expectedErrorLog = "error: ListObjectsV2"
			}

			err = fs.Unmount()
			So(err, ShouldBeNil)

			logs := fs.Logs()
			So(logs, ShouldNotBeNil)
			var foundExpectedLog bool
			var foundErrorLog bool
			for _, log := range logs {
				if strings.Contains(log, "info: ListObjectsV2") {
					foundExpectedLog = true
				}
				if expectedErrorLog != "" && strings.Contains(log, expectedErrorLog) {
					foundErrorLog = true
				}
			}
			So(foundExpectedLog, ShouldBeTrue)
			if expectedErrorLog != "" {
				So(foundErrorLog, ShouldBeTrue)
			}
		})

		Convey("You can mount without local file caching", t, func() {
			targetManual.CacheData = false
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount()
			So(err, ShouldBeNil)

			defer func() {
				err = fs.Unmount()
				So(err, ShouldBeNil)
			}()

			Convey("Listing mount directory and subdirs works", func() {
				s := time.Now()
				entries, err := ioutil.ReadDir(mountPoint)
				d := time.Since(s)
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				rootEntries := []string{"100k.lines:file:700000", "1G.file:file:1073741824", "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir"}
				So(details, ShouldResemble, rootEntries)

				// test it twice in a row to make sure caching is ok
				s = time.Now()
				entries, err = ioutil.ReadDir(mountPoint)
				dc := time.Since(s)
				So(err, ShouldBeNil)
				So(dc.Nanoseconds(), ShouldBeLessThan, d.Nanoseconds()/4)

				details = dirDetails(entries)
				So(details, ShouldResemble, rootEntries)

				// test the sub directories
				entries, err = ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)

				details = dirDetails(entries)
				So(details, ShouldResemble, []string{"deep:dir", "empty.file:file:0"})

				entries, err = ioutil.ReadDir(mountPoint + "/sub/deep")
				So(err, ShouldBeNil)

				details = dirDetails(entries)
				So(details, ShouldResemble, []string{"bar:file:4"})

				entries, err = ioutil.ReadDir(mountPoint + "/emptyDir")
				So(err, ShouldBeNil)

				details = dirDetails(entries)
				So(len(details), ShouldEqual, 0)
			})

			Convey("You can immediately list a subdir", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(details, ShouldResemble, []string{"deep:dir", "empty.file:file:0"})
			})

			Convey("You can immediately list an empty subdir", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/emptyDir")
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(len(details), ShouldEqual, 0)
			})

			Convey("Trying to list a non-existant subdir fails as expected", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/emptyDi")
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)
				details := dirDetails(entries)
				So(len(details), ShouldEqual, 0)
			})

			Convey("You can immediately list a deep subdir", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/sub/deep")
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(details, ShouldResemble, []string{"bar:file:4"})

				info, err := os.Stat(mountPoint + "/sub/deep/bar")
				So(err, ShouldBeNil)
				So(info.Name(), ShouldEqual, "bar")
				So(info.Size(), ShouldEqual, 4)
			})

			Convey("You can immediately stat a deep file", func() {
				info, err := os.Stat(mountPoint + "/sub/deep/bar")
				So(err, ShouldBeNil)
				So(info.Name(), ShouldEqual, "bar")
				So(info.Size(), ShouldEqual, 4)
			})

			Convey("You can read a whole file as well as parts of it by seeking", func() {
				path := mountPoint + "/100k.lines"
				read, err := streamFile(path, 0)
				So(err, ShouldBeNil)
				So(read, ShouldEqual, 700000)

				read, err = streamFile(path, 350000)
				So(err, ShouldBeNil)
				So(read, ShouldEqual, 350000)

				// make sure the contents are actually correct
				expected := ""
				for i := 1; i <= 100000; i++ {
					expected += fmt.Sprintf("%06d\n", i)
				}
				bytes, err := ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(string(bytes), ShouldEqual, expected)
			})

			Convey("You can do random reads on large files", func() {
				// sanity check that it works on a small file
				path := mountPoint + "/numalphanum.txt"
				r, err := os.Open(path)
				So(err, ShouldBeNil)
				defer r.Close()

				r.Seek(36, io.SeekStart)

				b := make([]byte, 10, 10)
				done, err := io.ReadFull(r, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 10)
				So(b, ShouldResemble, []byte("1234567890"))

				r.Seek(10, io.SeekStart)
				b = make([]byte, 10, 10)
				done, err = io.ReadFull(r, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 10)
				So(b, ShouldResemble, []byte("abcdefghij"))

				// it also works on a big one
				path = mountPoint + "/100k.lines"
				rbig, err := os.Open(path)
				So(err, ShouldBeNil)
				defer rbig.Close()

				rbig.Seek(350000, io.SeekStart)
				b = make([]byte, 6, 6)
				done, err = io.ReadFull(rbig, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 6)
				So(b, ShouldResemble, []byte("050001"))

				rbig.Seek(175000, io.SeekStart)
				b = make([]byte, 6, 6)
				done, err = io.ReadFull(rbig, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 6)
				So(b, ShouldResemble, []byte("025001"))
			})

			Convey("You can read a very big file", func() {
				ioutil.ReadDir(mountPoint) // we need to time reading the file, not stating it
				path := mountPoint + "/1G.file"
				start := time.Now()
				read, err := streamFile(path, 0)
				thisGetTime := time.Since(start)
				// fmt.Printf("\n1G file read took %s cached vs %s uncached\n", bigFileGetTime, thisGetTime)
				So(err, ShouldBeNil)
				So(read, ShouldEqual, 1073741824)
				So(math.Ceil(thisGetTime.Seconds()), ShouldBeLessThanOrEqualTo, math.Ceil(bigFileGetTime.Seconds())) // if it isn't, it's almost certainly a bug!
			})
		})

		Convey("You can mount multiple targets on the same mount point", t, func() {
			targetManual.CacheData = true
			targetManual2 := &Target{
				Target:    target + "/sub",
				AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
				SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
				CacheData: true,
			}

			cfgMultiplex := &Config{
				Mount:   mountPoint,
				Retries: 3,
				Verbose: false,
				Quiet:   false,
				Targets: []*Target{targetManual, targetManual2},
			}

			fs, err := New(cfgMultiplex)
			So(err, ShouldBeNil)

			err = fs.Mount()
			So(err, ShouldBeNil)

			defer func() {
				err = fs.Unmount()
				So(err, ShouldBeNil)
			}()

			Convey("Listing mount directory and subdirs works", func() {
				s := time.Now()
				entries, err := ioutil.ReadDir(mountPoint)
				d := time.Since(s)
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				rootEntries := []string{"100k.lines:file:700000", "1G.file:file:1073741824", "deep:dir", "empty.file:file:0", "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir"}
				So(details, ShouldResemble, rootEntries)

				// test it twice in a row to make sure caching is ok
				s = time.Now()
				entries, err = ioutil.ReadDir(mountPoint)
				dc := time.Since(s)
				So(err, ShouldBeNil)
				So(dc.Nanoseconds(), ShouldBeLessThan, d.Nanoseconds()/4)

				details = dirDetails(entries)
				So(details, ShouldResemble, rootEntries)

				// test the sub directories
				entries, err = ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)

				details = dirDetails(entries)
				So(details, ShouldResemble, []string{"deep:dir", "empty.file:file:0"})

				entries, err = ioutil.ReadDir(mountPoint + "/sub/deep")
				So(err, ShouldBeNil)

				details = dirDetails(entries)
				So(details, ShouldResemble, []string{"bar:file:4"})

				// and the sub dirs of the second mount
				entries, err = ioutil.ReadDir(mountPoint + "/deep")
				So(err, ShouldBeNil)

				details = dirDetails(entries)
				So(details, ShouldResemble, []string{"bar:file:4"})
			})

			Convey("You can immediately list a subdir", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(details, ShouldResemble, []string{"deep:dir", "empty.file:file:0"})
			})

			Convey("You can immediately list a subdir of the second target", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/deep")
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(details, ShouldResemble, []string{"bar:file:4"})

				info, err := os.Stat(mountPoint + "/deep/bar")
				So(err, ShouldBeNil)
				So(info.Name(), ShouldEqual, "bar")
				So(info.Size(), ShouldEqual, 4)
			})

			Convey("You can immediately list a deep subdir", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/sub/deep")
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(details, ShouldResemble, []string{"bar:file:4"})

				info, err := os.Stat(mountPoint + "/sub/deep/bar")
				So(err, ShouldBeNil)
				So(info.Name(), ShouldEqual, "bar")
				So(info.Size(), ShouldEqual, 4)
			})

			Convey("You can read files from both targets", func() {
				path := mountPoint + "/deep/bar"
				bytes, err := ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(string(bytes), ShouldEqual, "foo\n")

				path = mountPoint + "/sub/deep/bar"
				bytes, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(string(bytes), ShouldEqual, "foo\n")
			})
		})

		Convey("You can mount the bucket directly", t, func() {
			u, err := url.Parse(target)
			parts := strings.Split(u.Path[1:], "/")
			targetManual.Target = u.Scheme + "://" + u.Host + "/" + parts[0]
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount()
			So(err, ShouldBeNil)

			defer func() {
				err = fs.Unmount()
				So(err, ShouldBeNil)
			}()

			Convey("Listing bucket directory works", func() {
				entries, err := ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(details, ShouldContain, path.Join(parts[1:]...)+":dir")
			})

			Convey("You can't mount more than once at a time", func() {
				err = fs.Mount()
				So(err, ShouldNotBeNil)
			})
		})

		if strings.HasPrefix(target, "https://cog.sanger.ac.uk") {
			Convey("You can mount a public bucket", t, func() {
				targetManual.Target = "https://cog.sanger.ac.uk/npg-repository"
				fs, err := New(cfg)
				So(err, ShouldBeNil)

				err = fs.Mount()
				So(err, ShouldBeNil)

				defer func() {
					err = fs.Unmount()
					So(err, ShouldBeNil)
				}()

				Convey("Listing mount directory works", func() {
					entries, err := ioutil.ReadDir(mountPoint)
					So(err, ShouldBeNil)

					details := dirDetails(entries)
					So(details, ShouldContain, "cram_cache:dir")
					So(details, ShouldContain, "references:dir")
				})

				Convey("You can immediately stat deep files", func() {
					fasta := mountPoint + "/references/Homo_sapiens/GRCh38_full_analysis_set_plus_decoy_hla/all/fasta/Homo_sapiens.GRCh38_full_analysis_set_plus_decoy_hla"
					_, err := os.Stat(fasta + ".fa")
					So(err, ShouldBeNil)
					_, err = os.Stat(fasta + ".fa.alt")
					So(err, ShouldBeNil)
					_, err = os.Stat(fasta + ".fa.fai")
					So(err, ShouldBeNil)
					_, err = os.Stat(fasta + ".dict")
					So(err, ShouldBeNil)
				})
			})

			Convey("You can mount a public bucket at a deep path", t, func() {
				targetManual.Target = "https://cog.sanger.ac.uk/npg-repository/references/Homo_sapiens/GRCh38_full_analysis_set_plus_decoy_hla/all/fasta"
				fs, err := New(cfg)
				So(err, ShouldBeNil)

				err = fs.Mount()
				So(err, ShouldBeNil)

				defer func() {
					err = fs.Unmount()
					So(err, ShouldBeNil)
				}()

				Convey("Listing mount directory works", func() {
					entries, err := ioutil.ReadDir(mountPoint)
					So(err, ShouldBeNil)

					details := dirDetails(entries)
					So(details, ShouldContain, "Homo_sapiens.GRCh38_full_analysis_set_plus_decoy_hla.fa:file:3257948908")
				})

				Convey("You can immediately stat files within", func() {
					fasta := mountPoint + "/Homo_sapiens.GRCh38_full_analysis_set_plus_decoy_hla"
					_, err := os.Stat(fasta + ".fa")
					So(err, ShouldBeNil)
					_, err = os.Stat(fasta + ".fa.alt")
					So(err, ShouldBeNil)
					_, err = os.Stat(fasta + ".fa.fai")
					So(err, ShouldBeNil)
					_, err = os.Stat(fasta + ".dict")
					So(err, ShouldBeNil)
				})
			})

			Convey("You can mount a public bucket with blank credentials", t, func() {
				targetManual.Target = "https://cog.sanger.ac.uk/npg-repository"
				targetManual.AccessKey = ""
				targetManual.SecretKey = ""
				fs, err := New(cfg)
				So(err, ShouldBeNil)

				err = fs.Mount()
				So(err, ShouldBeNil)

				defer func() {
					err = fs.Unmount()
					So(err, ShouldBeNil)
				}()

				Convey("Listing mount directory works", func() {
					entries, err := ioutil.ReadDir(mountPoint)
					So(err, ShouldBeNil)

					details := dirDetails(entries)
					So(details, ShouldContain, "cram_cache:dir")
					So(details, ShouldContain, "references:dir")
				})
			})
		}
	}
}

func dirDetails(entries []os.FileInfo) (details []string) {
	for _, entry := range entries {
		info := entry.Name()
		if entry.IsDir() {
			info += ":dir"
		} else {
			info += fmt.Sprintf(":file:%d", entry.Size())
		}
		details = append(details, info)
	}
	sort.Slice(details, func(i, j int) bool { return details[i] < details[j] })
	return
}

func streamFile(src string, seek int64) (read int64, err error) {
	r, err := os.Open(src)
	if err != nil {
		return
	}
	if seek > 0 {
		r.Seek(seek, io.SeekStart)
	}
	read = stream(r)
	r.Close()
	return
}

func stream(r io.Reader) (read int64) {
	br := bufio.NewReader(r)
	b := make([]byte, 1000, 1000)
	for {
		done, rerr := br.Read(b)
		if rerr != nil {
			break
		}
		read += int64(done)
	}
	return
}
