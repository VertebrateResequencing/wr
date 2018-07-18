// Copyright © 2017, 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of muxfys.
//
//  muxfys is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  muxfys is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with muxfys. If not, see <http://www.gnu.org/licenses/>.

package muxfys

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/fuse"
	. "github.com/smartystreets/goconvey/convey"
)

func TestS3Localntegration(t *testing.T) {
	// We will create test files on local disk and then start up minio server
	// to give us an S3 system to test against.
	//
	// minio server can be installed by:
	// go get -u github.com/minio/minio
	//
	// These tests will only run if minio has already been installed and this
	// env var has been set: MUXFYS_S3_PORT (eg. set it to 9000)
	//
	// We must be able to start minio server on that port (ie. it can't already
	// be running for some other purpose).

	port := os.Getenv("MUXFYS_S3_PORT")
	_, lperr := exec.LookPath("minio")
	if port == "" || lperr != nil {
		SkipConvey("Without MUXFYS_S3_PORT environment variable and minio being installed, we'll skip local S3 tests", t, func() {})
		return
	}

	target := fmt.Sprintf("http://localhost:%s/user/wr_tests", port)
	accessKey := "AKIAIOSFODNN7EXAMPLE"
	secretKey := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	origAKey := os.Getenv("AWS_ACCESS_KEY_ID")
	origSKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	os.Setenv("AWS_ACCESS_KEY_ID", accessKey)
	os.Setenv("AWS_SECRET_ACCESS_KEY", secretKey)
	defer func() {
		os.Setenv("AWS_ACCESS_KEY_ID", origAKey)
		os.Setenv("AWS_SECRET_ACCESS_KEY", origSKey)
	}()

	// create our test files
	tmpdir, err := ioutil.TempDir("", "muxfys_testing")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpdir)

	err = os.Chdir(tmpdir) // because muxfys will create dirs relative to cwd
	if err != nil {
		log.Panic(err) // Panic instead of fatal so our deferred removal of tmpdir works
	}

	minioDir := filepath.Join(tmpdir, "minio")
	wrTestsDir := filepath.Join(minioDir, "user", "wr_tests")
	wrTestsSubDir := filepath.Join(wrTestsDir, "sub")
	wrTestsDeepDir := filepath.Join(wrTestsSubDir, "deep")
	err = os.MkdirAll(wrTestsDeepDir, os.FileMode(0700))
	if err != nil {
		log.Panic(err)
	}
	err = os.MkdirAll(filepath.Join(wrTestsDir, "emptyDir"), os.FileMode(0700))
	if err != nil {
		log.Panic(err)
	}

	bigFileSize := 10000000 // 10MB, also tested with 1GB and it's fine
	err = exec.Command("dd", "if=/dev/zero", "of="+filepath.Join(wrTestsDir, "big.file"), fmt.Sprintf("bs=%d", bigFileSize), "count=1").Run()
	if err != nil {
		log.Panic(err)
	}

	f, err := os.Create(filepath.Join(wrTestsDir, "numalphanum.txt"))
	if err != nil {
		log.Panic(err)
	}
	_, err = f.WriteString("1234567890abcdefghijklmnopqrstuvwxyz1234567890\n")
	if err != nil {
		log.Panic(err)
	}
	f.Close()

	f, err = os.Create(filepath.Join(wrTestsDir, "100k.lines"))
	if err != nil {
		log.Panic(err)
	}
	for i := 1; i <= 100000; i++ {
		_, err = f.WriteString(fmt.Sprintf("%06d\n", i))
		if err != nil {
			log.Panic(err)
		}
	}
	f.Close()

	f, err = os.Create(filepath.Join(wrTestsSubDir, "empty.file"))
	if err != nil {
		log.Panic(err)
	}
	f.Close()

	f, err = os.Create(filepath.Join(wrTestsDeepDir, "bar"))
	if err != nil {
		log.Panic(err)
	}
	_, err = f.WriteString("foo\n")
	if err != nil {
		log.Panic(err)
	}
	f.Close()

	// start minio
	os.Setenv("MINIO_ACCESS_KEY", accessKey)
	os.Setenv("MINIO_SECRET_KEY", secretKey)
	os.Setenv("MINIO_BROWSER", "off")
	minioCmd := exec.Command("minio", "server", "--address", fmt.Sprintf("localhost:%s", port), minioDir)

	// if all tests accessing what minio server is supposed to serve fail, debug
	// minio's startup:
	// go func() {
	// 	<-time.After(30 * time.Second)
	// 	minioCmd.Process.Kill()
	// 	minioCmd.Wait()
	// }()
	// out, err := minioCmd.CombinedOutput()
	// fmt.Println(string(out))
	// fmt.Println(err.Error())
	// fmt.Println(target)
	// return

	err = minioCmd.Start()
	if err != nil {
		log.Panic(err)
	}
	defer func() {
		minioCmd.Process.Kill()
		minioCmd.Wait()
	}()

	// give it time to become ready to respond to accesses
	if os.Getenv("CI") == "true" {
		<-time.After(30 * time.Second)
	} else {
		<-time.After(5 * time.Second)
	}

	s3IntegrationTests(t, tmpdir, target, accessKey, secretKey, bigFileSize, false)
}

func TestS3RemoteIntegration(t *testing.T) {
	// For these tests to work, MUXFYS_REMOTES3_TARGET must be the full URL to
	// an immediate child directory of a bucket that you have read and write
	// permissions for, eg: https://cog.domain.com/bucket/wr_tests You must also
	// have a ~/.s3cfg file with a [default] section specifying the same domain
	// and scheme via host_base and use_https.
	//
	// The child directory must contain the following:
	// perl -e 'for (1..100000) { printf "%06d\n", $_; }' > 100k.lines
	// echo 1234567890abcdefghijklmnopqrstuvwxyz1234567890 > numalphanum.txt
	// dd if=/dev/zero of=big.file bs=1073741824 count=1
	// mkdir -p sub/deep
	// touch sub/empty.file
	// echo foo > sub/deep/bar
	// export WR_BUCKET_SUB=s3://bucket/wr_tests
	// s3cmd put 100k.lines $WR_BUCKET_SUB/100k.lines
	// s3cmd put numalphanum.txt $WR_BUCKET_SUB/numalphanum.txt
	// s3cmd put big.file $WR_BUCKET_SUB/big.file
	// s3cmd put sub/empty.file $WR_BUCKET_SUB/sub/empty.file
	// s3cmd put sub/deep/bar $WR_BUCKET_SUB/sub/deep/bar
	// rm -fr 100k.lines numalphanum.txt big.file sub
	// [use s3fs to mkdir s3://bucket/wr_tests/emptyDir]

	target := os.Getenv("MUXFYS_REMOTES3_TARGET")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if target == "" || accessKey == "" || secretKey == "" {
		SkipConvey("Without MUXFYS_REMOTES3_TARGET, AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables, we'll skip remote S3 tests", t, func() {})
		return
	}

	tmpdir, err := ioutil.TempDir("", "muxfys_testing")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpdir)

	err = os.Chdir(tmpdir)
	if err != nil {
		log.Panic(err)
	}

	s3IntegrationTests(t, tmpdir, target, accessKey, secretKey, 1073741824, true)
}

func s3IntegrationTests(t *testing.T, tmpdir, target, accessKey, secretKey string, bigFileSize int, doRemoteTests bool) {
	// common configuration of muxfys
	mountPoint := filepath.Join(tmpdir, "mount")
	cacheDir := filepath.Join(tmpdir, "cacheDir")

	manualConfig := &S3Config{
		Target:    target,
		AccessKey: accessKey,
		SecretKey: secretKey,
	}
	accessor, errn := NewS3Accessor(manualConfig)
	if errn != nil {
		log.Panic(errn)
	}

	remoteConfig := &RemoteConfig{
		Accessor:  accessor,
		CacheData: true,
		Write:     false,
	}

	cfg := &Config{
		Mount:   mountPoint,
		Retries: 3,
		Verbose: false,
	}

	bigFileEntry := fmt.Sprintf("big.file:file:%d", bigFileSize)
	Convey("You can configure S3 from the environment", t, func() {
		envConfig, err := S3ConfigFromEnvironment("", "mybucket/subdir")
		So(err, ShouldBeNil)
		So(envConfig.AccessKey, ShouldEqual, manualConfig.AccessKey)
		So(envConfig.SecretKey, ShouldEqual, manualConfig.SecretKey)
		So(envConfig.Target, ShouldNotBeNil)
		So(envConfig.Target, ShouldEndWith, "mybucket/subdir")

		if doRemoteTests {
			u, _ := url.Parse(target)
			uNew := url.URL{
				Scheme: u.Scheme,
				Host:   u.Host,
				Path:   "mybucket/subdir",
			}
			So(envConfig.Target, ShouldEqual, uNew.String())

			envConfig2, err := S3ConfigFromEnvironment("default", "mybucket/subdir")
			So(err, ShouldBeNil)
			So(envConfig2.AccessKey, ShouldEqual, envConfig.AccessKey)
			So(envConfig2.SecretKey, ShouldEqual, envConfig.SecretKey)
			So(envConfig2.Target, ShouldEqual, envConfig.Target)

			_, err = S3ConfigFromEnvironment("-fake-", "mybucket/subdir")
			So(err, ShouldNotBeNil)

			// *** how can we test chaining of ~/.s3cfg and ~/.aws/credentials
			// without messing with those files?
		}
	})

	var bigFileGetTime time.Duration
	Convey("You can mount with local file caching", t, func() {
		fs, errc := New(cfg)
		So(errc, ShouldBeNil)

		errm := fs.Mount(remoteConfig)
		So(errm, ShouldBeNil)

		defer func() {
			erru := fs.Unmount()
			So(erru, ShouldBeNil)
		}()

		Convey("You can read a whole file as well as parts of it by seeking", func() {
			path := mountPoint + "/100k.lines"
			read, err := streamFile(path, 0)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, 700000)

			read, err = streamFile(path, 350000)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, 350000)

			// make sure the contents are actually correct
			var expected bytes.Buffer
			for i := 1; i <= 100000; i++ {
				expected.WriteString(fmt.Sprintf("%06d\n", i))
			}
			bytes, err := ioutil.ReadFile(path)
			So(err, ShouldBeNil)
			So(string(bytes), ShouldEqual, expected.String())
		})

		Convey("You can do random reads", func() {
			// it works on a small file
			path := mountPoint + "/numalphanum.txt"
			r, err := os.Open(path)
			So(err, ShouldBeNil)
			defer r.Close()

			r.Seek(36, io.SeekStart)

			b := make([]byte, 10)
			done, err := io.ReadFull(r, b)
			So(err, ShouldBeNil)
			So(done, ShouldEqual, 10)
			So(b, ShouldResemble, []byte("1234567890"))

			r.Seek(10, io.SeekStart)
			b = make([]byte, 10)
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
			b = make([]byte, 6)
			done, err = io.ReadFull(rbig, b)
			So(err, ShouldBeNil)
			So(done, ShouldEqual, 6)
			So(b, ShouldResemble, []byte("050001"))

			rbig.Seek(175000, io.SeekStart)
			b = make([]byte, 6)
			done, err = io.ReadFull(rbig, b)
			So(err, ShouldBeNil)
			So(done, ShouldEqual, 6)
			So(b, ShouldResemble, []byte("025001"))
		})

		Convey("You can read a very big file", func() {
			path := mountPoint + "/big.file"
			start := time.Now()
			read, err := streamFile(path, 0)
			bigFileGetTime = time.Since(start)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, bigFileSize)
		})

		Convey("Reading a small part of a very big file doesn't download the entire file", func() {
			path := mountPoint + "/big.file"
			t := time.Now()
			rbig, err := os.Open(path)
			So(err, ShouldBeNil)

			rbig.Seek(int64(bigFileSize/2), io.SeekStart)
			b := make([]byte, 6)
			done, err := io.ReadFull(rbig, b)
			So(err, ShouldBeNil)
			So(done, ShouldEqual, 6)
			rbig.Close()
			So(time.Since(t).Seconds(), ShouldBeLessThan, 1)

			cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("big.file"))
			stat, err := os.Stat(cachePath)
			So(err, ShouldBeNil)
			So(stat.Size(), ShouldEqual, bigFileSize)

			cmd := exec.Command("du", "-B1", "--apparent-size", cachePath)
			out, err := cmd.CombinedOutput()
			So(err, ShouldBeNil)
			So(string(out), ShouldStartWith, fmt.Sprintf("%d\t", bigFileSize))

			// even though we seeked to half way and only tried to read 6
			// bytes, the underlying system ends up sending a larger Read
			// request around the desired point, where the size depends on
			// the filesystem and other OS related things

			cmd = exec.Command("du", "-B1", cachePath)
			out, err = cmd.CombinedOutput()
			So(err, ShouldBeNil)
			parts := strings.Split(string(out), "\t")
			i, err := strconv.Atoi(parts[0])
			So(err, ShouldBeNil)
			So(i, ShouldBeGreaterThan, 6)
		})

		Convey("You can read different parts of a file simultaneously from 1 mount", func() {
			init := mountPoint + "/numalphanum.txt"
			path := mountPoint + "/100k.lines"

			// the first read takes longer than others, so read something
			// to "initialise" minio
			streamFile(init, 0)

			// first get a reference for how long it takes to read the whole
			// thing
			t2 := time.Now()
			read, errs := streamFile(path, 0)
			wt := time.Since(t2)
			So(errs, ShouldBeNil)
			So(read, ShouldEqual, 700000)

			// sanity check that re-reading uses our cache
			t2 = time.Now()
			streamFile(path, 0)
			st := time.Since(t2)

			// should have completed in under 90% of the time (since minio is
			// local, the difference in reading from cache vs from minio is just
			// the overhead of serving files from minio; if s3 was remote and
			// slow this would be more like 20%)
			et := time.Duration((wt.Nanoseconds()/100)*90) * time.Nanosecond
			So(st, ShouldBeLessThan, et)

			// remount to clear the cache
			erru := fs.Unmount()
			So(erru, ShouldBeNil)
			errm := fs.Mount(remoteConfig)
			So(errm, ShouldBeNil)
			streamFile(init, 0)

			// now read the whole file and half the file at the ~same time
			times := make(chan time.Duration, 2)
			errors := make(chan error, 2)
			streamer := func(offset, size int) {
				t := time.Now()
				thisRead, thisErr := streamFile(path, int64(offset))
				times <- time.Since(t)
				if thisErr != nil {
					errors <- thisErr
					return
				}
				if thisRead != int64(size) {
					errors <- fmt.Errorf("did not read %d bytes for offset %d (%d)", size, offset, thisRead)
					return
				}
				errors <- nil
			}

			t2 = time.Now()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				streamer(350000, 350000)
			}()
			go func() {
				defer wg.Done()
				streamer(0, 700000)
			}()
			wg.Wait()
			ot := time.Since(t2)

			// both should complete in not much more time than the slowest,
			// and that shouldn't be much slower than when reading alone
			// *** debugging shows that caching definitely is occurring as
			// expected, but I can't really prove it with these timings...
			So(<-errors, ShouldBeNil)
			So(<-errors, ShouldBeNil)
			pt1 := <-times
			pt2 := <-times
			eto := time.Duration((int64(math.Max(float64(pt1.Nanoseconds()), float64(pt2.Nanoseconds())))/100)*110) * time.Nanosecond
			// fmt.Printf("\nwt: %s, pt1: %s, pt2: %s, ot: %s, eto: %s, ets: %s\n", wt, pt1, pt2, ot, eto, ets)
			So(ot, ShouldBeLessThan, eto) // *** this can rarely fail, just have to repeat :(

			// *** unforunately the variability is too high, with both
			// pt1 and pt2 sometimes taking more than 2x longer to read
			// compared to wt, even though the below passes most of the time
			// ets := time.Duration((wt.Nanoseconds()/100)*150) * time.Nanosecond
			// So(ot, ShouldBeLessThan, ets)
		})

		Convey("You can read different files simultaneously from 1 mount", func() {
			init := mountPoint + "/numalphanum.txt"
			path1 := mountPoint + "/100k.lines"
			path2 := mountPoint + "/big.file"

			streamFile(init, 0)

			// first get a reference for how long it takes to read a certain
			// sized chunk of each file
			// t := time.Now()
			read, err := streamFile(path1, 0)
			// f1t := time.Since(t)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, 700000)

			// t = time.Now()
			read, err = streamFile(path2, int64(bigFileSize-700000))
			// f2t := time.Since(t)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, 700000)

			// remount to clear the cache
			err = fs.Unmount()
			So(err, ShouldBeNil)
			err = fs.Mount(remoteConfig)
			So(err, ShouldBeNil)
			streamFile(init, 0)

			// now repeat reading them at the ~same time
			times := make(chan time.Duration, 2)
			errors := make(chan error, 2)
			streamer := func(path string, offset, size int) {
				t := time.Now()
				thisRead, thisErr := streamFile(path, int64(offset))
				times <- time.Since(t)
				if thisErr != nil {
					errors <- thisErr
					return
				}
				if thisRead != int64(size) {
					errors <- fmt.Errorf("did not read %d bytes of %s at offset %d (%d)", size, path, offset, thisRead)
					return
				}
				errors <- nil
			}

			t := time.Now()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				streamer(path1, 0, 700000)
			}()
			go func() {
				defer wg.Done()
				streamer(path2, int(bigFileSize-700000), 700000)
			}()
			wg.Wait()
			ot := time.Since(t)

			// each should have completed in less than 190% of the time
			// needed to read them sequentially, and both should have
			// completed in less than 110% of the slowest one
			So(<-errors, ShouldBeNil)
			So(<-errors, ShouldBeNil)
			pt1 := <-times
			pt2 := <-times
			// et1 := time.Duration((f1t.Nanoseconds()/100)*190) * time.Nanosecond
			// et2 := time.Duration((f2t.Nanoseconds()/100)*190) * time.Nanosecond
			var multiplier int64
			if bigFileSize > 10000000 {
				multiplier = 110
			} else {
				multiplier = 250
			}
			eto := time.Duration((int64(math.Max(float64(pt1.Nanoseconds()), float64(pt2.Nanoseconds())))/100)*multiplier) * time.Nanosecond
			// *** these timing tests are too unreliable when using minio server
			// So(pt1, ShouldBeLessThan, et1)
			// So(pt2, ShouldBeLessThan, et2)
			So(ot, ShouldBeLessThan, eto)
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
			path := mountPoint + "/big.file"
			err := os.Remove(path)
			So(err, ShouldNotBeNil)
			perr, ok := err.(*os.PathError)
			So(ok, ShouldBeTrue)
			So(perr.Error(), ShouldContainSubstring, "operation not permitted")
		})

		Convey("And you can't rename files", func() {
			path := mountPoint + "/big.file"
			dest := mountPoint + "/1G.moved"
			cmd := exec.Command("mv", path, dest)
			err := cmd.Run()
			So(err, ShouldNotBeNil)
		})

		Convey("You can't touch files in non Write mode", func() {
			path := mountPoint + "/big.file"
			cmd := exec.Command("touch", path)
			err := cmd.Run()
			So(err, ShouldNotBeNil)
		})

		Convey("You can't make, delete or rename directories in non Write mode", func() {
			newDir := mountPoint + "/newdir_test"
			cmd := exec.Command("mkdir", newDir)
			err := cmd.Run()
			So(err, ShouldNotBeNil)

			path := mountPoint + "/sub"
			cmd = exec.Command("rmdir", path)
			err = cmd.Run()
			So(err, ShouldNotBeNil)

			cmd = exec.Command("mv", path, newDir)
			err = cmd.Run()
			So(err, ShouldNotBeNil)
		})

		Convey("Unmounting after reading a file deletes the cache dir", func() {
			streamFile(mountPoint+"/numalphanum.txt", 0)
			thisCacheDir := fs.remotes[0].cacheDir
			_, err := os.Stat(thisCacheDir)
			So(err, ShouldBeNil)
			err = fs.Unmount()
			So(err, ShouldBeNil)
			_, err = os.Stat(thisCacheDir)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
		})
	})

	Convey("You can mount with local file caching in write mode", t, func() {
		remoteConfig.Write = true
		fs, errc := New(cfg)
		So(errc, ShouldBeNil)

		errm := fs.Mount(remoteConfig)
		So(errm, ShouldBeNil)

		defer func() {
			erru := fs.Unmount()
			remoteConfig.Write = false
			So(erru, ShouldBeNil)
		}()

		Convey("Trying to write in write mode works", func() {
			path := mountPoint + "/write.test"
			b := []byte("write test\n")
			errf := ioutil.WriteFile(path, b, 0644)
			So(errf, ShouldBeNil)

			// you can immediately read it back
			bytes, errf := ioutil.ReadFile(path)
			So(errf, ShouldBeNil)
			So(bytes, ShouldResemble, b)

			// (because it's in the the local cache)
			thisCacheDir := fs.remotes[0].cacheDir
			_, errf = os.Stat(thisCacheDir)
			So(errf, ShouldBeNil)
			cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
			_, errf = os.Stat(cachePath)
			So(errf, ShouldBeNil)

			// and it's statable and listable
			_, errf = os.Stat(path)
			So(errf, ShouldBeNil)

			entries, errf := ioutil.ReadDir(mountPoint)
			So(errf, ShouldBeNil)
			details := dirDetails(entries)
			rootEntries := []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test:file:11"}
			So(details, ShouldResemble, rootEntries)

			// unmounting causes the local cached file to be deleted
			errf = fs.Unmount()
			So(errf, ShouldBeNil)

			_, errf = os.Stat(cachePath)
			So(errf, ShouldNotBeNil)
			So(os.IsNotExist(errf), ShouldBeTrue)
			_, errf = os.Stat(thisCacheDir)
			So(errf, ShouldNotBeNil)
			So(os.IsNotExist(errf), ShouldBeTrue)
			_, errf = os.Stat(path)
			So(errf, ShouldNotBeNil)
			So(os.IsNotExist(errf), ShouldBeTrue)

			// remounting lets us read the file again - it actually got
			// uploaded
			errf = fs.Mount(remoteConfig)
			So(errf, ShouldBeNil)

			cachePath = fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
			_, errf = os.Stat(cachePath)
			So(errf, ShouldNotBeNil)
			So(os.IsNotExist(errf), ShouldBeTrue)

			bytes, errf = ioutil.ReadFile(path)
			So(errf, ShouldBeNil)
			So(bytes, ShouldResemble, b)

			_, errf = os.Stat(cachePath)
			So(errf, ShouldBeNil)

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

				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				bytes, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(string(bytes), ShouldEqual, string(b)+line2)

				Convey("You can truncate a cached file", func() {
					err := os.Truncate(path, 0)
					So(err, ShouldBeNil)

					cachePath2 := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					stat, err := os.Stat(cachePath2)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 0)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 0)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					_, err = os.Stat(cachePath2)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount(remoteConfig)
					So(err, ShouldBeNil)

					cachePath2 = fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					_, err = os.Stat(cachePath2)
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

						_, err = os.Stat(cachePath2)
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

					cachePath2 := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					stat, err := os.Stat(cachePath2)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 3)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 3)

					err = fs.Unmount()
					So(err, ShouldBeNil)

					_, err = os.Stat(cachePath2)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount(remoteConfig)
					So(err, ShouldBeNil)

					cachePath2 = fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					_, err = os.Stat(cachePath2)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					stat, err = os.Stat(path)
					So(err, ShouldBeNil)
					So(stat.Size(), ShouldEqual, 3)
					bytes, err = ioutil.ReadFile(path)
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

					bytes, err = ioutil.ReadFile(path)
					So(err, ShouldBeNil)
					So(string(bytes), ShouldEqual, line)

					cachePath2 := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
					err = fs.Unmount()
					So(err, ShouldBeNil)

					_, err = os.Stat(cachePath2)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)
					_, err = os.Stat(path)
					So(err, ShouldNotBeNil)
					So(os.IsNotExist(err), ShouldBeTrue)

					err = fs.Mount(remoteConfig)
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
				err := cmd.Run()
				So(err, ShouldBeNil)

				bytes, err = ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)

				_, err = os.Stat(path)
				So(err, ShouldNotBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
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
				err := fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				dest := mountPoint + "/write.moved"
				err = os.Rename(path, dest)
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
				err = fs.Mount(remoteConfig)
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
				err = fs.Mount(remoteConfig)
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
			err := cmd.Run()
			So(err, ShouldNotBeNil)
		})

		Convey("You can't remove remote directories", func() {
			subDir := mountPoint + "/sub"
			cmd := exec.Command("rmdir", subDir)
			err := cmd.Run()
			So(err, ShouldNotBeNil)
		})

		Convey("You can create directories and rename and remove those", func() {
			newDir := mountPoint + "/newdir_test"
			cmd := exec.Command("mkdir", newDir)
			err := cmd.Run()
			So(err, ShouldBeNil)

			entries, err := ioutil.ReadDir(mountPoint)
			So(err, ShouldBeNil)
			details := dirDetails(entries)
			rootEntries := []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "newdir_test:dir", "numalphanum.txt:file:47", "sub:dir"}
			So(details, ShouldResemble, rootEntries)

			movedDir := mountPoint + "/newdir_moved"
			cmd = exec.Command("mv", newDir, movedDir)
			err = cmd.Run()
			So(err, ShouldBeNil)

			entries, err = ioutil.ReadDir(mountPoint)
			So(err, ShouldBeNil)
			details = dirDetails(entries)
			rootEntries = []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "newdir_moved:dir", "numalphanum.txt:file:47", "sub:dir"}
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
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				bytes, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)

				os.Remove(path)
			})
		})

		Convey("Given a local directory", func() {
			mvDir := filepath.Join(tmpdir, "mvtest")
			mvSubDir := filepath.Join(mvDir, "mvsubdir")
			errf := os.MkdirAll(mvSubDir, os.FileMode(0700))
			So(errf, ShouldBeNil)
			mvFile := filepath.Join(mvSubDir, "file")
			mvBytes := []byte("mvfile\n")
			errf = ioutil.WriteFile(mvFile, mvBytes, 0644)
			So(errf, ShouldBeNil)
			errf = ioutil.WriteFile(filepath.Join(mvDir, "a.file"), mvBytes, 0644)
			So(errf, ShouldBeNil)

			Convey("You can mv it to the mount point", func() {
				mountDir := filepath.Join(mountPoint, "mvtest")
				dest := filepath.Join(mountDir, "mvsubdir", "file")
				dest2 := filepath.Join(mountDir, "a.file")

				cmd := exec.Command("mv", mvDir, mountDir)
				err := cmd.Run()
				So(err, ShouldBeNil)

				bytes, err := ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)

				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(dest)
					So(err, ShouldBeNil)
					err = os.Remove(dest2)
					So(err, ShouldBeNil)
				}()

				bytes, err = ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)
			})

			Convey("You can mv its contents to the mount point", func() {
				dest := filepath.Join(mountPoint, "mvsubdir", "file")
				dest2 := filepath.Join(mountPoint, "a.file")

				cmd := exec.Command("sh", "-c", fmt.Sprintf("mv %s/* %s/", mvDir, mountPoint))
				err := cmd.Run()
				So(err, ShouldBeNil)

				bytes, err := ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)

				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(dest)
					So(err, ShouldBeNil)
					err = os.Remove(dest2)
					So(err, ShouldBeNil)
				}()

				bytes, err = ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)
			})
		})

		Convey("Trying to read a non-existent file fails as expected", func() {
			name := "non-existent.file"
			path := mountPoint + "/" + name
			_, err := streamFile(path, 0)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
		})

		Convey("Trying to read an externally deleted file fails as expected", func() {
			name := "non-existent.file"
			path := mountPoint + "/" + name
			// we'll hack fs to make it think non-existent.file does exist
			// so we can test the behaviour of a file getting deleted
			// externally
			//ioutil.ReadDir(mountPoint) // *** can't figure out why this causes a race condition
			fs.GetAttr("/", &fuse.Context{})
			So(fs.files["big.file"], ShouldNotBeNil)
			So(fs.files[name], ShouldBeNil)
			fs.mapMutex.Lock()
			fs.addNewEntryToItsDir(name, fuse.S_IFREG)
			fs.files[name] = fs.files["big.file"]
			fs.fileToRemote[name] = fs.fileToRemote["big.file"]
			fs.mapMutex.Unlock()
			So(fs.files[name], ShouldNotBeNil)
			_, err := streamFile(path, 0)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
			So(fs.files[name], ShouldNotBeNil) // *** unfortunately we only know it doesn't exist when we try to read, which means we can't update fs
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

			err = fs.Mount(remoteConfig)
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

				cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
				err = fs.Unmount()
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)
				_, err = os.Stat(path)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				bytes, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(string(bytes), ShouldEqual, string(b)+line2)
			})

			Convey("You can append to an uncached file and upload without reading the original part of the file", func() {
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
				So(err, ShouldBeNil)

				line2 := "line2\n"
				_, err = f.WriteString(line2)
				f.Close()
				So(err, ShouldBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				bytes, err := ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(string(bytes), ShouldEqual, string(b)+line2)
			})

			Convey("You can append to a partially read file", func() {
				// first make the file bigger so we can avoid minimum file
				// read size issues
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
				So(err, ShouldBeNil)

				for i := 2; i <= 10000; i++ {
					_, err = f.WriteString(fmt.Sprintf("line%d\n", i))
					if err != nil {
						break
					}
				}
				f.Close()
				So(err, ShouldBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				// now do a partial read
				r, err := os.Open(path)
				So(err, ShouldBeNil)

				r.Seek(11, io.SeekStart)

				b := make([]byte, 5)
				done, err := io.ReadFull(r, b)
				r.Close()
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 5)
				So(string(b), ShouldEqual, "line2")

				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 88899)

				// now append
				f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
				So(err, ShouldBeNil)

				newline := "line10001\n"
				_, err = f.WriteString(newline)
				f.Close()
				So(err, ShouldBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				// check it worked correctly
				info, err = os.Stat(path)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 88909)

				r, err = os.Open(path)
				So(err, ShouldBeNil)

				r.Seek(11, io.SeekStart)

				b = make([]byte, 5)
				done, err = io.ReadFull(r, b)
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 5)
				So(string(b), ShouldEqual, "line2")

				r.Seek(88889, io.SeekStart)

				b = make([]byte, 19)
				done, err = io.ReadFull(r, b)
				r.Close()
				So(err, ShouldBeNil)
				So(done, ShouldEqual, 19)
				So(string(b), ShouldEqual, "line10000\nline10001")
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

				_, err = os.Stat(cachePath)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
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

				_, err = os.Stat(cachePath)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
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

				_, err = os.Stat(cachePath)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
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

				cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
				err = fs.Unmount()
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)
				_, err = os.Stat(path)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				err = fs.Mount(remoteConfig)
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

				cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("write.test"))
				err = fs.Unmount()
				So(err, ShouldBeNil)

				_, err = os.Stat(cachePath)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)
				_, err = os.Stat(path)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				bytes, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)
				So(string(bytes), ShouldEqual, line)
			})

			Convey("You can write to the mount point and immediately delete the file and get the correct listing", func() {
				entries, err := ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)
				details := dirDetails(entries)
				subEntries := []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test:file:11"}
				So(details, ShouldResemble, subEntries)

				path2 := mountPoint + "/write.test2"
				b := []byte("write test2\n")
				err = ioutil.WriteFile(path2, b, 0644)
				So(err, ShouldBeNil)

				// it's statable and listable
				_, err = os.Stat(path2)
				So(err, ShouldBeNil)

				entries, err = ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)
				details = dirDetails(entries)
				subEntries = []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test2:file:12", "write.test:file:11"}
				So(details, ShouldResemble, subEntries)

				// once deleted, it's no longer listed
				err = os.Remove(path2)
				So(err, ShouldBeNil)

				_, err = os.Stat(path2)
				So(err, ShouldNotBeNil)

				entries, err = ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)
				details = dirDetails(entries)
				subEntries = []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test:file:11"}
				So(details, ShouldResemble, subEntries)

				// running unix ls reveals problems that ReadDir doesn't
				cmd := exec.Command("ls", "-alth", mountPoint)
				err = cmd.Run()
				So(err, ShouldBeNil)
			})

			Convey("You can touch an uncached file", func() {
				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				cmd := exec.Command("touch", "-d", "2006-01-02 15:04:05", path)
				err = cmd.Run()
				So(err, ShouldBeNil)
				info2, err := os.Stat(path)
				So(err, ShouldBeNil)
				So(info.ModTime().Unix(), ShouldNotAlmostEqual, info2.ModTime().Unix(), 62)
				So(info2.ModTime().String(), ShouldStartWith, "2006-01-02 15:04:05 +0000")
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
				So(err, ShouldBeNil)
				So(info.ModTime().String(), ShouldStartWith, "2006-01-02 15:04:05 +0000")
			})

			Convey("You can touch a cached file", func() {
				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				_, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)

				cmd := exec.Command("touch", "-d", "2006-01-02 15:04:05", path)
				err = cmd.Run()
				So(err, ShouldBeNil)
				info2, err := os.Stat(path)
				So(err, ShouldBeNil)
				So(info.ModTime().Unix(), ShouldNotAlmostEqual, info2.ModTime().Unix(), 62)
				So(info2.ModTime().String(), ShouldStartWith, "2006-01-02 15:04:05 +0000")

				cmd = exec.Command("touch", "-d", "2007-01-02 15:04:05", path)
				err = cmd.Run()
				So(err, ShouldBeNil)
				info3, err := os.Stat(path)
				So(err, ShouldBeNil)
				So(info2.ModTime().Unix(), ShouldNotAlmostEqual, info3.ModTime().Unix(), 62)
				So(info3.ModTime().String(), ShouldStartWith, "2007-01-02 15:04:05 +0000")
			})

			Convey("You can directly change the mtime on a cached file", func() {
				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				_, err = ioutil.ReadFile(path)
				So(err, ShouldBeNil)

				t := time.Now().Add(5 * time.Minute)
				err = os.Chtimes(path, t, t)
				So(err, ShouldBeNil)
				info2, err := os.Stat(path)
				So(err, ShouldBeNil)
				So(info.ModTime().Unix(), ShouldNotAlmostEqual, info2.ModTime().Unix(), 62)
				So(info2.ModTime().Unix(), ShouldAlmostEqual, t.Unix(), 2)
			})

			Convey("But not an uncached one", func() {
				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				t := time.Now().Add(5 * time.Minute)
				err = os.Chtimes(path, t, t)
				So(err, ShouldBeNil)
				info2, err := os.Stat(path)
				So(err, ShouldBeNil)
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
			err = fs.Mount(remoteConfig)
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
			err = fs.Mount(remoteConfig)
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
			err := cmd.Run()
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

			err = fs.Mount(remoteConfig)
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

		Convey("You can't create hard links", func() {
			source := mountPoint + "/numalphanum.txt"
			dest := mountPoint + "/link.hard"
			err := os.Link(source, dest)
			So(err, ShouldNotBeNil)
		})

		Convey("You can create and use symbolic links", func() {
			source := mountPoint + "/numalphanum.txt"
			dest := mountPoint + "/link.soft"
			err := os.Symlink(source, dest)
			So(err, ShouldBeNil)
			bytes, err := ioutil.ReadFile(dest)
			So(err, ShouldBeNil)
			So(string(bytes), ShouldEqual, "1234567890abcdefghijklmnopqrstuvwxyz1234567890\n")

			info, err := os.Lstat(dest)
			So(err, ShouldBeNil)
			So(info.Size(), ShouldEqual, 7)

			d, err := os.Readlink(dest)
			So(err, ShouldBeNil)
			So(d, ShouldEqual, source)

			Convey("But they're not uploaded", func() {
				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				_, err = os.Stat(dest)
				So(err, ShouldNotBeNil)
			})

			Convey("You can delete them", func() {
				err = os.Remove(dest)
				So(err, ShouldBeNil)
				_, err = os.Stat(dest)
				So(err, ShouldNotBeNil)
			})
		})
	})

	Convey("You can mount with local file caching in an explicit location", t, func() {
		remoteConfig.CacheDir = cacheDir
		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		defer func() {
			err = fs.Unmount()
			remoteConfig.CacheDir = ""
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

		Convey("You can read different parts of a file simultaneously from 1 mount, and it's only downloaded once", func() {
			init := mountPoint + "/numalphanum.txt"
			path := mountPoint + "/100k.lines"

			// the first read takes longer than others, so read something
			// to "initialise" minio
			streamFile(init, 0)

			// first get a reference for how long it takes to read the whole
			// thing
			t := time.Now()
			read, err := streamFile(path, 0)
			wt := time.Since(t)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, 700000)

			// sanity check that re-reading uses our cache
			t = time.Now()
			streamFile(path, 0)
			st := time.Since(t)

			// should have completed in under 90% of the time
			et := time.Duration((wt.Nanoseconds()/100)*99) * time.Nanosecond
			So(st, ShouldBeLessThan, et)

			// remount to clear the cache
			err = fs.Unmount()
			So(err, ShouldBeNil)
			err = fs.Mount(remoteConfig)
			So(err, ShouldBeNil)
			streamFile(init, 0)

			// now read the whole file and half the file at the ~same time
			times := make(chan time.Duration, 2)
			errors := make(chan error, 2)
			streamer := func(offset, size int) {
				t2 := time.Now()
				thisRead, thisErr := streamFile(path, int64(offset))
				times <- time.Since(t2)
				if thisErr != nil {
					errors <- thisErr
					return
				}
				if thisRead != int64(size) {
					errors <- fmt.Errorf("did not read %d bytes for offset %d (%d)", size, offset, thisRead)
					return
				}
				errors <- nil
			}

			t = time.Now()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				streamer(350000, 350000)
			}()
			go func() {
				defer wg.Done()
				streamer(0, 700000)
			}()
			wg.Wait()
			ot := time.Since(t)

			// both should complete in not much more time than the slowest,
			// and that shouldn't be much slower than when reading alone
			// *** debugging shows that the file is only downloaded once,
			// but don't have a good way of proving that here
			So(<-errors, ShouldBeNil)
			So(<-errors, ShouldBeNil)
			pt1 := <-times
			pt2 := <-times
			var multiplier int64
			if runtime.NumCPU() == 1 {
				multiplier = 240
			} else {
				multiplier = 190
			}
			eto := time.Duration((int64(math.Max(float64(pt1.Nanoseconds()), float64(pt2.Nanoseconds())))/100)*multiplier) * time.Nanosecond
			// ets := time.Duration((wt.Nanoseconds()/100)*160) * time.Nanosecond
			// fmt.Printf("\nwt: %s, pt1: %s, pt2: %s, ot: %s, eto: %s, ets: %s\n", wt, pt1, pt2, ot, eto, ets)
			So(ot, ShouldBeLessThan, eto)
			// So(ot, ShouldBeLessThan, ets)
		})

		Convey("You can read different files simultaneously from 1 mount", func() {
			init := mountPoint + "/numalphanum.txt"
			path1 := mountPoint + "/100k.lines"
			path2 := mountPoint + "/big.file"

			streamFile(init, 0)

			// first get a reference for how long it takes to read a certain
			// sized chunk of each file
			// t := time.Now()
			read, err := streamFile(path1, 0)
			// f1t := time.Since(t)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, 700000)

			// t = time.Now()
			read, err = streamFile(path2, int64(bigFileSize-700000))
			// f2t := time.Since(t)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, 700000)

			// remount to clear the cache
			err = fs.Unmount()
			So(err, ShouldBeNil)
			err = fs.Mount(remoteConfig)
			So(err, ShouldBeNil)
			streamFile(init, 0)

			// now repeat reading them at the ~same time
			times := make(chan time.Duration, 2)
			errors := make(chan error, 2)
			streamer := func(path string, offset, size int) {
				t := time.Now()
				thisRead, thisErr := streamFile(path, int64(offset))
				times <- time.Since(t)
				if thisErr != nil {
					errors <- thisErr
					return
				}
				if thisRead != int64(size) {
					errors <- fmt.Errorf("did not read %d bytes of %s at offset %d (%d)", size, path, offset, thisRead)
					return
				}
				errors <- nil
			}

			t := time.Now()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				streamer(path1, 0, 700000)
			}()
			go func() {
				defer wg.Done()
				streamer(path2, int(bigFileSize-700000), 700000)
			}()
			wg.Wait()
			ot := time.Since(t)

			// each should have completed in less than 190% of the time
			// needed to read them sequentially, and both should have
			// completed in less than 110% of the slowest one
			So(<-errors, ShouldBeNil)
			So(<-errors, ShouldBeNil)
			pt1 := <-times
			pt2 := <-times
			// et1 := time.Duration((f1t.Nanoseconds()/100)*190) * time.Nanosecond
			// et2 := time.Duration((f2t.Nanoseconds()/100)*190) * time.Nanosecond
			var multiplier int64
			if bigFileSize > 10000000 {
				if runtime.NumCPU() == 1 {
					multiplier = 200
				} else {
					multiplier = 110
				}
			} else {
				if runtime.NumCPU() == 1 {
					multiplier = 350
				} else {
					multiplier = 250
				}
			}
			eto := time.Duration((int64(math.Max(float64(pt1.Nanoseconds()), float64(pt2.Nanoseconds())))/100)*multiplier) * time.Nanosecond
			// *** these timing tests are too unreliable when using minio server
			// So(pt1, ShouldBeLessThan, et1)
			// So(pt2, ShouldBeLessThan, et2)
			So(ot, ShouldBeLessThan, eto)
		})
	})

	Convey("You can mount with local file caching in an explicit relative location", t, func() {
		remoteConfig.CacheDir = ".muxfys_test_cache_dir"
		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		defer func() {
			err = fs.Unmount()
			So(err, ShouldBeNil)
			os.RemoveAll(remoteConfig.CacheDir)
			remoteConfig.CacheDir = ""
		}()

		path := mountPoint + "/numalphanum.txt"
		_, err = ioutil.ReadFile(path)
		So(err, ShouldBeNil)

		cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("numalphanum.txt"))
		_, err = os.Stat(cachePath)
		So(err, ShouldBeNil)
		cwd, _ := os.Getwd()
		So(cachePath, ShouldStartWith, filepath.Join(cwd, ".muxfys_test_cache_dir"))

		Convey("Unmounting doesn't delete the cache", func() {
			err = fs.Unmount()
			So(err, ShouldBeNil)

			_, err = os.Stat(cachePath)
			So(err, ShouldBeNil)
		})
	})

	Convey("You can mount with local file caching relative to the home directory", t, func() {
		remoteConfig.CacheDir = "~/.wr_muxfys_test_cache_dir"
		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		defer func() {
			err = fs.Unmount()
			remoteConfig.CacheDir = ""
			So(err, ShouldBeNil)
			os.RemoveAll(filepath.Join(os.Getenv("HOME"), ".wr_muxfys_test_cache_dir"))
		}()

		path := mountPoint + "/numalphanum.txt"
		_, err = ioutil.ReadFile(path)
		So(err, ShouldBeNil)

		cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("numalphanum.txt"))
		_, err = os.Stat(cachePath)
		So(err, ShouldBeNil)

		So(cachePath, ShouldStartWith, filepath.Join(os.Getenv("HOME"), ".wr_muxfys_test_cache_dir"))

		Convey("Unmounting doesn't delete the cache", func() {
			err = fs.Unmount()
			So(err, ShouldBeNil)

			_, err = os.Stat(cachePath)
			So(err, ShouldBeNil)
		})
	})

	Convey("You can mount with a relative mount point", t, func() {
		cfg.Mount = ".wr_muxfys_test_mount_dir"
		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		defer func() {
			err = fs.Unmount()
			So(err, ShouldBeNil)
			os.RemoveAll(cfg.Mount)
			cfg.Mount = mountPoint
		}()

		path := filepath.Join(cfg.Mount, "numalphanum.txt")
		_, err = ioutil.ReadFile(path)
		So(err, ShouldBeNil)
	})

	Convey("You can mount with a ~/ mount point", t, func() {
		cfg.Mount = "~/.wr_muxfys_test_mount_dir"
		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		defer func() {
			err = fs.Unmount()
			cfg.Mount = mountPoint
			So(err, ShouldBeNil)
			os.RemoveAll(filepath.Join(os.Getenv("HOME"), ".wr_muxfys_test_mount_dir"))
		}()

		path := filepath.Join(os.Getenv("HOME"), ".wr_muxfys_test_mount_dir", "numalphanum.txt")
		_, err = ioutil.ReadFile(path)
		So(err, ShouldBeNil)
	})

	Convey("You can mount with no defined mount point", t, func() {
		cfg.Mount = ""
		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		defer func() {
			err = fs.Unmount()
			cfg.Mount = mountPoint
			So(err, ShouldBeNil)
			os.RemoveAll("mnt")
		}()

		path := filepath.Join("mnt", "numalphanum.txt")
		_, err = ioutil.ReadFile(path)
		So(err, ShouldBeNil)
	})

	Convey("You can't mount on a non-empty directory", t, func() {
		cfg.Mount = os.Getenv("HOME")
		_, err := New(cfg)
		defer func() {
			cfg.Mount = mountPoint
		}()
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "not empty")
	})

	Convey("You can mount in write mode and not upload on unmount", t, func() {
		remoteConfig.Write = true
		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		defer func() {
			err = fs.Unmount()
			remoteConfig.Write = false
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
		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		_, err = os.Stat(cachePath)
		So(err, ShouldNotBeNil)
		So(os.IsNotExist(err), ShouldBeTrue)

		_, err = os.Stat(path)
		So(err, ShouldNotBeNil)
		So(os.IsNotExist(err), ShouldBeTrue)
	})

	Convey("You can mount with verbose to get more logs", t, func() {
		origVerbose := cfg.Verbose
		cfg.Verbose = true
		defer func() {
			cfg.Verbose = origVerbose
		}()
		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig)
		So(err, ShouldBeNil)

		_, err = ioutil.ReadDir(mountPoint)
		expectedErrorLog := ""
		if err != nil {
			expectedErrorLog = "ListEntries"
		}

		err = fs.Unmount()
		So(err, ShouldBeNil)

		logs := fs.Logs()
		So(logs, ShouldNotBeNil)
		var foundExpectedLog bool
		var foundErrorLog bool
		for _, log := range logs {
			if strings.Contains(log, "ListEntries") {
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

	var bigFileGetTimeUncached time.Duration
	Convey("You can mount without local file caching", t, func() {
		remoteConfig.CacheData = false
		fs, errc := New(cfg)
		So(errc, ShouldBeNil)

		errm := fs.Mount(remoteConfig)
		So(errm, ShouldBeNil)

		defer func() {
			erru := fs.Unmount()
			So(erru, ShouldBeNil)
		}()

		cachePath := fs.remotes[0].getLocalPath(fs.remotes[0].getRemotePath("big.file"))
		So(cachePath, ShouldBeBlank)

		Convey("Listing mount directory and subdirs works", func() {
			s := time.Now()
			entries, err := ioutil.ReadDir(mountPoint)
			d := time.Since(s)
			So(err, ShouldBeNil)

			details := dirDetails(entries)
			rootEntries := []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir"}
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

			if doRemoteTests {
				entries, err = ioutil.ReadDir(mountPoint + "/emptyDir")
				So(err, ShouldBeNil)

				details = dirDetails(entries)
				So(len(details), ShouldEqual, 0)
			}
		})

		Convey("You can immediately list a subdir", func() {
			entries, err := ioutil.ReadDir(mountPoint + "/sub")
			So(err, ShouldBeNil)

			details := dirDetails(entries)
			So(details, ShouldResemble, []string{"deep:dir", "empty.file:file:0"})
		})

		if doRemoteTests {
			Convey("You can immediately list an empty subdir", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/emptyDir")
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(len(details), ShouldEqual, 0)
			})
		}

		Convey("Trying to list a non-existent subdir fails as expected", func() {
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
			var expected bytes.Buffer
			for i := 1; i <= 100000; i++ {
				expected.WriteString(fmt.Sprintf("%06d\n", i))
			}
			bytes, err := ioutil.ReadFile(path)
			So(err, ShouldBeNil)
			So(string(bytes), ShouldEqual, expected.String())
		})

		Convey("You can do random reads on large files", func() {
			// sanity check that it works on a small file
			path := mountPoint + "/numalphanum.txt"
			r, err := os.Open(path)
			So(err, ShouldBeNil)
			defer r.Close()

			r.Seek(36, io.SeekStart)

			b := make([]byte, 10)
			done, err := io.ReadFull(r, b)
			So(err, ShouldBeNil)
			So(done, ShouldEqual, 10)
			So(b, ShouldResemble, []byte("1234567890"))

			r.Seek(10, io.SeekStart)
			b = make([]byte, 10)
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
			b = make([]byte, 6)
			done, err = io.ReadFull(rbig, b)
			So(err, ShouldBeNil)
			So(done, ShouldEqual, 6)
			So(b, ShouldResemble, []byte("050001"))

			rbig.Seek(175000, io.SeekStart)
			b = make([]byte, 6)
			done, err = io.ReadFull(rbig, b)
			So(err, ShouldBeNil)
			So(done, ShouldEqual, 6)
			So(b, ShouldResemble, []byte("025001"))
		})

		Convey("You can read a very big file", func() {
			ioutil.ReadDir(mountPoint) // we need to time reading the file, not stating it
			path := mountPoint + "/big.file"
			start := time.Now()
			read, err := streamFile(path, 0)
			bigFileGetTimeUncached = time.Since(start)
			// fmt.Printf("\n1G file read took %s cached vs %s uncached\n", bigFileGetTime, thisGetTime)
			So(err, ShouldBeNil)
			So(read, ShouldEqual, bigFileSize)
			So(math.Ceil(bigFileGetTimeUncached.Seconds()), ShouldBeLessThanOrEqualTo, math.Ceil(bigFileGetTime.Seconds())+2) // if it isn't, it's almost certainly a bug!
		})

		Convey("You can read a very big file using cat", func() {
			ioutil.ReadDir(mountPoint)
			path := mountPoint + "/big.file"
			start := time.Now()
			err := exec.Command("cat", path).Run()
			So(err, ShouldBeNil)
			thisGetTime := time.Since(start)
			// fmt.Printf("\n1G file cat took %s \n", thisGetTime)
			So(err, ShouldBeNil)

			// it should be about as quick as the above streamFile call: much
			// slower means a bug
			So(math.Ceil(thisGetTime.Seconds()), ShouldBeLessThanOrEqualTo, math.Ceil(bigFileGetTimeUncached.Seconds())+2)
		})

		Convey("You can read a very big file using cp", func() {
			ioutil.ReadDir(mountPoint)
			path := mountPoint + "/big.file"
			start := time.Now()
			err := exec.Command("cp", path, "/dev/null").Run()
			So(err, ShouldBeNil)
			thisGetTime := time.Since(start)
			// fmt.Printf("\n1G file cp took %s \n", thisGetTime)
			So(err, ShouldBeNil)

			// it should be about as quick as the above streamFile call: much
			// slower means a bug
			So(math.Ceil(thisGetTime.Seconds()), ShouldBeLessThanOrEqualTo, math.Ceil(bigFileGetTimeUncached.Seconds())+2)
		})

		Convey("Trying to read a non-existent file fails as expected", func() {
			name := "non-existent.file"
			path := mountPoint + "/" + name
			_, err := streamFile(path, 0)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
		})

		Convey("Trying to read an externally deleted file fails as expected", func() {
			name := "non-existent.file"
			path := mountPoint + "/" + name
			// we'll hack fs to make it think non-existent.file does exist
			// so we can test the behaviour of a file getting deleted
			// externally
			fs.GetAttr("/", &fuse.Context{})
			fs.mapMutex.Lock()
			fs.addNewEntryToItsDir(name, fuse.S_IFREG)
			fs.files[name] = fs.files["big.file"]
			fs.fileToRemote[name] = fs.fileToRemote["big.file"]
			fs.mapMutex.Unlock()
			_, err := streamFile(path, 0)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
		})
	})

	Convey("You can mount in write mode without any caching", t, func() {
		remoteConfig.Write = true
		remoteConfig.CacheData = false
		fs, errc := New(cfg)
		So(errc, ShouldBeNil)

		errm := fs.Mount(remoteConfig)
		So(errm, ShouldBeNil)

		defer func() {
			erru := fs.Unmount()
			remoteConfig.Write = false
			So(erru, ShouldBeNil)
		}()

		Convey("Trying to write works", func() {
			path := mountPoint + "/write.test"
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

			entries, err := ioutil.ReadDir(mountPoint)
			So(err, ShouldBeNil)
			details := dirDetails(entries)
			rootEntries := []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test:file:11"}
			So(details, ShouldResemble, rootEntries)

			err = fs.Unmount()
			So(err, ShouldBeNil)

			_, err = os.Stat(path)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)

			// remounting lets us read the file again - it actually got
			// uploaded
			err = fs.Mount(remoteConfig)
			So(err, ShouldBeNil)

			bytes, err = ioutil.ReadFile(path)
			So(err, ShouldBeNil)
			So(bytes, ShouldResemble, b)
		})

		if bigFileSize > 10000000 {
			// *** -race flag isn't passed through to us, so can't limit this skip to just -race
			SkipConvey("Writing a very large file breaks machines with race detector on", func() {})
		} else {
			Convey("Writing a large file works", func() {
				// because minio-go currently uses a ~600MB bytes.Buffer (2x
				// allocation growth) during streaming upload, and dd itself creates
				// an input buffer of the size bs, we have to give dd a small bs and
				// increase the count instead. This way we don't run out of memory
				// even when bigFileSize is greater than physical memory on the
				// machine

				path := mountPoint + "/write.test"
				err := exec.Command("dd", "if=/dev/zero", "of="+path, fmt.Sprintf("bs=%d", bigFileSize/1000), "count=1000").Run()
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(path)
					So(err, ShouldBeNil)
				}()

				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				expectedSize := (bigFileSize / 1000) * 1000
				So(info.Size(), ShouldEqual, expectedSize)

				err = fs.Unmount()
				So(err, ShouldBeNil)

				_, err = os.Stat(path)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				info, err = os.Stat(path)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, expectedSize)
			})
		}

		Convey("Given a local directory", func() {
			mvDir := filepath.Join(tmpdir, "mvtest2")
			mvSubDir := filepath.Join(mvDir, "mvsubdir")
			errf := os.MkdirAll(mvSubDir, os.FileMode(0700))
			So(errf, ShouldBeNil)
			mvFile := filepath.Join(mvSubDir, "file")
			mvBytes := []byte("mvfile\n")
			errf = ioutil.WriteFile(mvFile, mvBytes, 0644)
			So(errf, ShouldBeNil)
			errf = ioutil.WriteFile(filepath.Join(mvDir, "a.file"), mvBytes, 0644)
			So(errf, ShouldBeNil)

			Convey("You can mv it to the mount point", func() {
				mountDir := filepath.Join(mountPoint, "mvtest2")
				dest := filepath.Join(mountDir, "mvsubdir", "file")
				dest2 := filepath.Join(mountDir, "a.file")

				cmd := exec.Command("sh", "-c", fmt.Sprintf("mv %s %s/", mvDir, mountPoint))
				out, err := cmd.CombinedOutput()
				So(err, ShouldBeNil)
				So(len(out), ShouldEqual, 0)

				bytes, err := ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)

				bytes, err = ioutil.ReadFile(dest2)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)

				_, err = os.Stat(filepath.Join(mountPoint, "mvtest2", "mvsubdir"))
				So(err, ShouldBeNil)
				_, err = os.Stat(mountDir)
				So(err, ShouldBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(dest)
					So(err, ShouldBeNil)
					err = os.Remove(dest2)
					So(err, ShouldBeNil)
				}()

				bytes, err = ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)
			})

			Convey("You can mv its contents to the mount point", func() {
				dest := filepath.Join(mountPoint, "mvsubdir", "file")
				dest2 := filepath.Join(mountPoint, "a.file")

				cmd := exec.Command("sh", "-c", fmt.Sprintf("mv %s/* %s/", mvDir, mountPoint))
				err := cmd.Run()
				So(err, ShouldBeNil)

				bytes, err := ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)

				err = fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig)
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(dest)
					So(err, ShouldBeNil)
					err = os.Remove(dest2)
					So(err, ShouldBeNil)
				}()

				bytes, err = ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, mvBytes)
			})
		})
	})

	Convey("You can mount multiple remotes on the same mount point", t, func() {
		remoteConfig.CacheData = true
		manualConfig2 := &S3Config{
			Target:    target + "/sub",
			AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		}
		accessor, errn = NewS3Accessor(manualConfig2)
		So(errn, ShouldBeNil)
		remoteConfig2 := &RemoteConfig{
			Accessor:  accessor,
			CacheData: true,
		}

		fs, err := New(cfg)
		So(err, ShouldBeNil)

		err = fs.Mount(remoteConfig, remoteConfig2)
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
			rootEntries := []string{"100k.lines:file:700000", bigFileEntry, "deep:dir", "empty.file:file:0", "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir"}
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

		Convey("You can immediately list a subdir of the second remote", func() {
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

		Convey("You can read files from both remotes", func() {
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
		u, errp := url.Parse(target)
		So(errp, ShouldBeNil)
		parts := strings.Split(u.Path[1:], "/")
		manualConfig.Target = u.Scheme + "://" + u.Host + "/" + parts[0]
		accessor, errn = NewS3Accessor(manualConfig)
		So(errn, ShouldBeNil)
		remoteConfig := &RemoteConfig{
			Accessor:  accessor,
			CacheData: true,
			Write:     false,
		}
		fs, errc := New(cfg)
		So(errc, ShouldBeNil)

		errm := fs.Mount(remoteConfig)
		So(errm, ShouldBeNil)

		defer func() {
			erru := fs.Unmount()
			So(erru, ShouldBeNil)
		}()

		Convey("Listing bucket directory works", func() {
			entries, err := ioutil.ReadDir(mountPoint)
			So(err, ShouldBeNil)

			details := dirDetails(entries)
			So(details, ShouldContain, path.Join(parts[1:]...)+":dir")
		})

		Convey("You can't mount more than once at a time", func() {
			err := fs.Mount(remoteConfig)
			So(err, ShouldNotBeNil)
		})
	})

	Convey("For non-existent paths...", t, func() {
		manualConfig.Target = target + "/nonexistent/subdir"
		accessor, errn = NewS3Accessor(manualConfig)
		So(errn, ShouldBeNil)

		Convey("You can mount them read-only", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: true,
				Write:     false,
			}
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount(remoteConfig)
			So(err, ShouldBeNil)
			defer fs.Unmount()

			Convey("Getting the contents of the dir works", func() {
				entries, err := ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)
				So(len(entries), ShouldEqual, 0)
			})
		})

		Convey("You can mount them writeable and writes work", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: false,
				Write:     true,
			}
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount(remoteConfig)
			defer func() {
				err = fs.Unmount()
				So(err, ShouldBeNil)
			}()
			So(err, ShouldBeNil)

			entries, err := ioutil.ReadDir(mountPoint)
			So(err, ShouldBeNil)
			So(len(entries), ShouldEqual, 0)

			path := mountPoint + "/write.test"
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

			entries, err = ioutil.ReadDir(mountPoint)
			So(err, ShouldBeNil)
			details := dirDetails(entries)
			rootEntries := []string{"write.test:file:11"}
			So(details, ShouldResemble, rootEntries)

			err = fs.Unmount()
			So(err, ShouldBeNil)

			_, err = os.Stat(path)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)

			// remounting lets us read the file again - it actually got
			// uploaded
			err = fs.Mount(remoteConfig)
			So(err, ShouldBeNil)

			bytes, err = ioutil.ReadFile(path)
			So(err, ShouldBeNil)
			So(bytes, ShouldResemble, b)
		})

		Convey("You can mount a non-empty dir for reading and a non-existant dir for writing", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: false,
				Write:     true,
			}

			manualConfig2 := &S3Config{
				Target:    target,
				AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
				SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			}
			accessor, errn = NewS3Accessor(manualConfig2)
			So(errn, ShouldBeNil)
			remoteConfig2 := &RemoteConfig{
				Accessor:  accessor,
				CacheData: false,
			}

			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount(remoteConfig2, remoteConfig)
			defer func() {
				err = fs.Unmount()
				So(err, ShouldBeNil)
			}()
			So(err, ShouldBeNil)

			entries, err := ioutil.ReadDir(mountPoint)
			So(err, ShouldBeNil)
			So(len(entries), ShouldEqual, 5)

			details := dirDetails(entries)
			rootEntries := []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir"}
			So(details, ShouldResemble, rootEntries)

			Convey("Reads and writes work", func() {
				source := mountPoint + "/numalphanum.txt"
				dest := mountPoint + "/write.test"
				err := exec.Command("cp", source, dest).Run()
				So(err, ShouldBeNil)

				defer func() {
					err = os.Remove(dest)
					So(err, ShouldBeNil)
				}()

				// you can immediately read it back
				bytes, err := ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				b := []byte("1234567890abcdefghijklmnopqrstuvwxyz1234567890\n")
				So(bytes, ShouldResemble, b)

				// and it's statable and listable
				_, err = os.Stat(dest)
				So(err, ShouldBeNil)

				entries, err = ioutil.ReadDir(mountPoint)
				So(err, ShouldBeNil)
				details := dirDetails(entries)
				rootEntries := []string{"100k.lines:file:700000", bigFileEntry, "emptyDir:dir", "numalphanum.txt:file:47", "sub:dir", "write.test:file:47"}
				So(details, ShouldResemble, rootEntries)

				err = fs.Unmount()
				So(err, ShouldBeNil)

				_, err = os.Stat(dest)
				So(err, ShouldNotBeNil)
				So(os.IsNotExist(err), ShouldBeTrue)

				// remounting lets us read the file again - it actually got
				// uploaded
				err = fs.Mount(remoteConfig2, remoteConfig)
				So(err, ShouldBeNil)

				bytes, err = ioutil.ReadFile(dest)
				So(err, ShouldBeNil)
				So(bytes, ShouldResemble, b)
			})
		})
	})

	if strings.HasPrefix(target, "https://cog.sanger.ac.uk") {
		Convey("You can mount a public bucket", t, func() {
			manualConfig.Target = "https://cog.sanger.ac.uk/npg-repository"
			accessor, errn = NewS3Accessor(manualConfig)
			So(errn, ShouldBeNil)
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: true,
				Write:     false,
			}
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount(remoteConfig)
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
			manualConfig.Target = "https://cog.sanger.ac.uk/npg-repository/references/Homo_sapiens/GRCh38_full_analysis_set_plus_decoy_hla/all/fasta"
			accessor, errn = NewS3Accessor(manualConfig)
			So(errn, ShouldBeNil)
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: true,
				Write:     false,
			}
			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount(remoteConfig)
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

		Convey("You can multiplex different buckets", t, func() {
			manualConfig2 := &S3Config{
				Target:    target + "/sub",
				AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
				SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			}
			accessor2, err := NewS3Accessor(manualConfig2)
			So(err, ShouldBeNil)
			remoteConfig2 := &RemoteConfig{
				Accessor:  accessor2,
				CacheData: false,
			}

			manualConfig.Target = "https://cog.sanger.ac.uk/npg-repository/references/Homo_sapiens/GRCh38_full_analysis_set_plus_decoy_hla/all/fasta"
			accessor, err = NewS3Accessor(manualConfig)
			So(err, ShouldBeNil)
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: true,
				Write:     false,
			}

			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount(remoteConfig, remoteConfig2)
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
				So(details, ShouldContain, "empty.file:file:0")
			})

			Convey("You can immediately stat files within", func() {
				_, err := os.Stat(mountPoint + "/Homo_sapiens.GRCh38_full_analysis_set_plus_decoy_hla.fa")
				So(err, ShouldBeNil)
				_, err = os.Stat(mountPoint + "/empty.file")
				So(err, ShouldBeNil)
			})
		})

		Convey("You can mount a public bucket with blank credentials", t, func() {
			manualConfig.Target = "https://cog.sanger.ac.uk/npg-repository"
			manualConfig.AccessKey = ""
			manualConfig.SecretKey = ""
			accessor, errn = NewS3Accessor(manualConfig)
			So(errn, ShouldBeNil)
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: true,
				Write:     false,
			}

			fs, err := New(cfg)
			So(err, ShouldBeNil)

			err = fs.Mount(remoteConfig)
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

func dirDetails(entries []os.FileInfo) []string {
	var details []string
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
	return details
}

func streamFile(src string, seek int64) (read int64, err error) {
	r, err := os.Open(src)
	if err != nil {
		return read, err
	}
	if seek > 0 {
		r.Seek(seek, io.SeekStart)
	}
	read, err = stream(r)
	r.Close()
	return read, err
}

func stream(r io.Reader) (read int64, err error) {
	br := bufio.NewReader(r)
	b := make([]byte, 1000)
	for {
		done, rerr := br.Read(b)
		if rerr != nil {
			if rerr != io.EOF {
				err = rerr
			}
			break
		}
		read += int64(done)
	}
	return read, err
}
