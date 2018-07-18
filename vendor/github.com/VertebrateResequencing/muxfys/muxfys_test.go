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
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	. "github.com/smartystreets/goconvey/convey"
)

var uploadFail bool
var resetMutex sync.Mutex
var resetFail bool

// openedObject is what a localAccessor returns from its OpenFile() method. It
// is just a wrapper around the return value from os.Open that allows us to
// make reads fails at will, for testing purposes.
type openedObject struct {
	object *os.File
}

func (f *openedObject) Read(b []byte) (int, error) {
	resetMutex.Lock()
	defer resetMutex.Unlock()
	if resetFail {
		return 0, fmt.Errorf("connection reset by peer")
	}
	return f.object.Read(b)
}

func (f *openedObject) Seek(offset int64, whence int) (int64, error) {
	return f.object.Seek(offset, whence)
}

func (f *openedObject) Close() error {
	return f.object.Close()
}

// localAccessor implements RemoteAccessor: it just accesses the local POSIX
// file system for testing purposes.
type localAccessor struct {
	target string
}

func (a *localAccessor) copyFile(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := filepath.Dir(dest)
	err = os.MkdirAll(dir, 0700)
	if err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// DownloadFile implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) DownloadFile(source, dest string) (err error) {
	return a.copyFile(source, dest)
}

// UploadFile implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) UploadFile(source, dest, contentType string) error {
	if uploadFail {
		return fmt.Errorf("upload failed")
	}
	return a.copyFile(source, dest)
}

// UploadData implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) UploadData(data io.Reader, dest string) error {
	if uploadFail {
		return fmt.Errorf("upload failed")
	}
	dir := filepath.Dir(dest)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, data); err != nil {
		return err
	}
	return out.Sync()
}

// ListEntries implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) ListEntries(dir string) ([]RemoteAttr, error) {
	resetMutex.Lock()
	defer resetMutex.Unlock()
	if resetFail {
		return nil, fmt.Errorf("connection reset by peer")
	}
	entries, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ras []RemoteAttr
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		ras = append(ras, RemoteAttr{
			Name:  dir + name,
			Size:  entry.Size(),
			MTime: entry.ModTime(),
		})
	}
	return ras, err
}

// OpenFile implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) OpenFile(path string, offset int64) (io.ReadCloser, error) {
	resetMutex.Lock()
	defer resetMutex.Unlock()
	if resetFail {
		return nil, fmt.Errorf("connection reset by peer")
	}
	f, err := os.Open(path)
	if offset > 0 {
		f.Seek(offset, io.SeekStart)
	}
	return &openedObject{object: f}, err
}

// Seek implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) Seek(path string, rc io.ReadCloser, offset int64) (io.ReadCloser, error) {
	object := rc.(*openedObject)
	_, err := object.Seek(offset, io.SeekStart)
	return object, err
}

// CopyFile implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) CopyFile(source, dest string) error {
	return a.copyFile(source, dest)
}

// DeleteFile implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) DeleteFile(path string) error {
	return os.Remove(path)
}

// DeleteIncompleteUpload implements RemoteAccessor by deferring to local fs.
func (a *localAccessor) DeleteIncompleteUpload(path string) error {
	return os.Remove(path)
}

// ErrorIsNotExists implements RemoteAccessor by deferring to os.
func (a *localAccessor) ErrorIsNotExists(err error) bool {
	return os.IsNotExist(err)
}

// ErrorIsNoQuota implements RemoteAccessor by deferring to os.
func (a *localAccessor) ErrorIsNoQuota(err error) bool {
	return false // *** is there a standard error for running out of disk space?
}

// Target implements RemoteAccessor by returning the initial target we were
// configured with.
func (a *localAccessor) Target() string {
	return a.target
}

// RemotePath implements RemoteAccessor by using the initially configured target.
func (a *localAccessor) RemotePath(relPath string) string {
	return filepath.Join(a.target, relPath)
}

// LocalPath implements RemoteAccessor by adding nothing extra.
func (a *localAccessor) LocalPath(baseDir, remotePath string) string {
	return filepath.Join(baseDir, remotePath)
}

func TestMuxFys(t *testing.T) {
	user, errt := user.Current()
	if errt != nil {
		log.Fatal(errt)
	}

	// *** the cache deletion tests no longer work on nfs, don't know why!
	// pwd, err := os.Getwd() // doing these tests from an nfs mounted home dir reveals some bugs that were fixed
	// if err != nil {
	//  log.Fatal(err)
	// }

	tmpdir, errt := ioutil.TempDir("", "muxfys_testing")
	if errt != nil {
		log.Fatal(errt)
	}
	defer os.RemoveAll(tmpdir)

	errt = os.Chdir(tmpdir)
	if errt != nil {
		log.Fatal(errt)
	}
	cacheBase := filepath.Join(tmpdir, "cacheBase")
	os.MkdirAll(cacheBase, os.FileMode(0777))

	cachePermanent := filepath.Join(tmpdir, "cachePermanent")
	os.MkdirAll(cachePermanent, os.FileMode(0777))

	sourcePoint := filepath.Join(tmpdir, "source")
	os.MkdirAll(sourcePoint, os.FileMode(0777))
	errt = ioutil.WriteFile(filepath.Join(sourcePoint, "read.file"), []byte("test1\ntest2\n"), 0644)
	if errt != nil {
		log.Fatal(errt)
	}

	sourceOtherDir := filepath.Join(sourcePoint, "other")
	os.MkdirAll(sourceOtherDir, os.FileMode(0777))
	errt = ioutil.WriteFile(filepath.Join(sourceOtherDir, "read2.file"), []byte("test\n"), 0644)
	if errt != nil {
		log.Fatal(errt)
	}

	f, errt := os.Create(filepath.Join(sourcePoint, "large.file"))
	if errt != nil {
		log.Fatal(errt)
	}
	for i := 1; i <= 10000; i++ {
		_, errt = f.WriteString(fmt.Sprintf("test%d\n", i))
		if errt != nil {
			log.Fatal(errt)
		}
	}
	f.Sync()
	f.Close()

	accessor := &localAccessor{
		target: sourcePoint,
	}

	sourceSubDir := filepath.Join(sourcePoint, "subdir")
	accessorNonExistent := &localAccessor{
		target: sourceSubDir,
	}

	// for testing purposes we override exitFunc and deathSignals
	var i int
	var efm sync.Mutex
	exitFunc = func(code int) {
		efm.Lock()
		defer efm.Unlock()
		i = code
	}
	deathSignals = []os.Signal{syscall.SIGUSR1}

	Convey("You can make a New MuxFys with an explicit Mount", t, func() {
		explicitMount := filepath.Join(tmpdir, "explicitMount")
		cfg := &Config{
			Mount:     explicitMount,
			CacheBase: cacheBase,
			Verbose:   true,
			Retries:   2,
		}
		fs, errn := New(cfg)
		So(errn, ShouldBeNil)

		Convey("You can Mount() read-only uncached", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: false,
				Write:     false,
			}
			errm := fs.Mount(remoteConfig)
			So(errm, ShouldBeNil)
			defer fs.Unmount()

			Convey("Once mounted you can't mount again", func() {
				err := fs.Mount(remoteConfig)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldEqual, "Can't mount more that once at a time")
			})

			Convey("You can Unmount()", func() {
				err := fs.Unmount()
				So(err, ShouldBeNil)
			})

			Convey("You can UnmountOnDeath()", func() {
				So(fs.handlingSignals, ShouldBeFalse)
				fs.UnmountOnDeath()
				So(fs.handlingSignals, ShouldBeTrue)
				So(fs.mounted, ShouldBeTrue)
				So(i, ShouldEqual, 0)

				// doing it again is harmless
				fs.UnmountOnDeath()

				syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
				<-time.After(500 * time.Millisecond)

				fs.mutex.Lock()
				defer fs.mutex.Unlock()
				So(fs.mounted, ShouldBeFalse)
				efm.Lock()
				defer efm.Unlock()
				So(i, ShouldEqual, 1)
				i = 0
			})

			Convey("You can Unmount() while UnmountOnDeath() is active", func() {
				fs.UnmountOnDeath()
				So(fs.mounted, ShouldBeTrue)
				So(i, ShouldEqual, 0)

				err := fs.Unmount()
				So(err, ShouldBeNil)
				So(fs.mounted, ShouldBeFalse)

				syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
				<-time.After(500 * time.Millisecond)

				So(i, ShouldEqual, 0)
			})

			Convey("Reads are retried on connection reset by peer", func() {
				f, err := os.Open(filepath.Join(explicitMount, "large.file"))
				So(err, ShouldBeNil)
				b := make([]byte, 6)
				_, err = f.Read(b)
				So(err, ShouldBeNil)
				So(string(b), ShouldEqual, "test1\n")

				defer func() {
					f.Close()
					fs.Unmount()
				}()

				f.Seek(60003, 0)

				resetMutex.Lock()
				resetFail = true
				resetMutex.Unlock()
				go func() {
					<-time.After(3 * time.Second)
					resetMutex.Lock()
					resetFail = false
					resetMutex.Unlock()
				}()

				before := time.Now()
				b = make([]byte, 9)
				_, err = f.Read(b)
				after := time.Since(before)
				So(err, ShouldBeNil)
				So(string(b), ShouldEqual, "test6791\n")
				So(after.Seconds(), ShouldBeGreaterThanOrEqualTo, 3)
			})
		})

		Convey("You can Mount() writable cached", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: true,
				Write:     true,
			}
			errm := fs.Mount(remoteConfig)
			So(errm, ShouldBeNil)
			defer fs.Unmount()

			Convey("You can Unmount()", func() {
				err := fs.Unmount()
				So(err, ShouldBeNil)
				So(checkEmpty(cacheBase), ShouldBeTrue)
			})

			Convey("Unmount() after reading files fully deletes the cache dir", func() {
				data, err := ioutil.ReadFile(filepath.Join(explicitMount, "read.file"))
				So(err, ShouldBeNil)
				So(string(data), ShouldEqual, "test1\ntest2\n")
				err = fs.Unmount()
				So(err, ShouldBeNil)
				So(checkEmpty(cacheBase), ShouldBeTrue)
			})

			Convey("Unmounting after creating files uploads them", func() {
				sourceFile1 := filepath.Join(sourcePoint, "created1.file")
				_, err := os.Stat(sourceFile1)
				So(err, ShouldNotBeNil)
				sourceFile2 := filepath.Join(sourcePoint, "created2.file")
				_, err = os.Stat(sourceFile2)
				So(err, ShouldNotBeNil)

				f, err := os.OpenFile(filepath.Join(explicitMount, "created1.file"), os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)
				f.Close()
				defer os.Remove(sourceFile1)
				f, err = os.OpenFile(filepath.Join(explicitMount, "created2.file"), os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)
				f.Close()
				defer os.Remove(sourceFile2)

				// they don't exist prior to unmount
				_, err = os.Stat(sourceFile1)
				So(err, ShouldNotBeNil)
				_, err = os.Stat(sourceFile2)
				So(err, ShouldNotBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)

				_, err = os.Stat(sourceFile1)
				So(err, ShouldBeNil)
				_, err = os.Stat(sourceFile2)
				So(err, ShouldBeNil)

				Convey("SetLogHandler() lets you log events", func() {
					recs := make(chan *log15.Record, 10)
					SetLogHandler(log15.ChannelHandler(recs))

					err := fs.Mount(remoteConfig)
					So(err, ShouldBeNil)

					_, err = os.Stat(filepath.Join(explicitMount, "created1.file"))
					So(err, ShouldBeNil)

					rec := <-recs
					So(rec.Ctx[7], ShouldEqual, "ListEntries")
					SetLogHandler(log15.DiscardHandler())
					close(recs)
				})
			})

			Convey("Unmounting reports failure to upload", func() {
				sourceFile := filepath.Join(sourcePoint, "created.file")
				_, err := os.Stat(sourceFile)
				So(err, ShouldNotBeNil)

				f, err := os.OpenFile(filepath.Join(explicitMount, "created.file"), os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)
				f.Close()

				uploadFail = true
				defer func() {
					uploadFail = false
				}()
				defer os.Remove(sourceFile)

				err = fs.Unmount()
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldEqual, "failed to upload 1 files")

				Convey("Logs() tells you what happened", func() {
					logs := fs.Logs()
					So(len(logs), ShouldEqual, 2)
					So(logs[1], ShouldContainSubstring, "lvl=eror")
					So(logs[1], ShouldContainSubstring, `msg="Remote call failed"`)
					So(logs[1], ShouldContainSubstring, "pkg=muxfys")
					So(logs[1], ShouldContainSubstring, "mount="+explicitMount)
					So(logs[1], ShouldContainSubstring, "target="+sourcePoint)
					So(logs[1], ShouldContainSubstring, "call=UploadFile")
					So(logs[1], ShouldContainSubstring, "path="+sourceFile)
					So(logs[1], ShouldContainSubstring, "retries=2")
					So(logs[1], ShouldContainSubstring, "walltime=")
					So(logs[1], ShouldContainSubstring, `err="upload failed"`)
					So(logs[1], ShouldContainSubstring, "caller=remote.go")
				})
			})

			Convey("We try the desired number of times to access bad remotes", func() {
				resetMutex.Lock()
				resetFail = true
				resetMutex.Unlock()
				defer func() {
					resetMutex.Lock()
					resetFail = false
					resetMutex.Unlock()
				}()

				entries, err := ioutil.ReadDir(explicitMount)
				So(err, ShouldBeNil) // *** not sure why this doesn't give an err
				So(len(entries), ShouldEqual, 0)

				Convey("Logs() tells you what happened", func() {
					logs := fs.Logs()
					So(len(logs), ShouldEqual, 1)
					So(logs[0], ShouldContainSubstring, "lvl=eror")
					So(logs[0], ShouldContainSubstring, `msg="Remote call failed"`)
					So(logs[0], ShouldContainSubstring, "pkg=muxfys")
					So(logs[0], ShouldContainSubstring, "mount="+explicitMount)
					So(logs[0], ShouldContainSubstring, "target="+sourcePoint)
					So(logs[0], ShouldContainSubstring, "call=ListEntries")
					So(logs[0], ShouldContainSubstring, "path=/")
					So(logs[0], ShouldContainSubstring, "retries=2")
					So(logs[0], ShouldContainSubstring, `err="connection reset by peer"`)
				})
			})

			Convey("We try greater than the desired number of times to access a good remote that turns bad", func() {
				entries, err := ioutil.ReadDir(explicitMount)
				So(err, ShouldBeNil)
				So(len(entries), ShouldEqual, 3)

				resetMutex.Lock()
				resetFail = true
				resetMutex.Unlock()
				go func() {
					<-time.After(1 * time.Second)
					resetMutex.Lock()
					resetFail = false
					resetMutex.Unlock()
				}()

				entries, err = ioutil.ReadDir(explicitMount + "/other")
				So(err, ShouldBeNil)
				So(len(entries), ShouldEqual, 1)

				Convey("Logs() tells you what happened", func() {
					logs := fs.Logs()
					So(len(logs), ShouldBeGreaterThanOrEqualTo, 5)
					So(logs[1], ShouldContainSubstring, "lvl=warn")
					So(logs[1], ShouldContainSubstring, "call=ListEntries")
					So(logs[1], ShouldContainSubstring, `err="connection reset by peer"`)
					So(logs[1], ShouldContainSubstring, `retries=0`)
					lastLog := logs[len(logs)-1]
					So(lastLog, ShouldContainSubstring, "lvl=info")
					So(lastLog, ShouldContainSubstring, "call=ListEntries")
					So(lastLog, ShouldContainSubstring, `previous_err="connection reset by peer"`)
					moreRetries := false
					if strings.Contains(lastLog, "retries=3") || strings.Contains(lastLog, "retries=4") || strings.Contains(lastLog, "retries=5") {
						moreRetries = true
					}
					So(moreRetries, ShouldBeTrue)
				})
			})

			Convey("You can't have 2 writeable remotes", func() {
				err := fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig, remoteConfig)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldEqual, "You can't have more than one writeable remote")
			})

			Convey("UnmountOnDeath() will exit(2) on failure to unmount", func() {
				fs.UnmountOnDeath()
				So(fs.mounted, ShouldBeTrue)
				So(i, ShouldEqual, 0)

				f, err := os.OpenFile(filepath.Join(explicitMount, "opened.file"), os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)

				syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
				<-time.After(500 * time.Millisecond)

				So(fs.mounted, ShouldBeTrue)
				efm.Lock()
				defer efm.Unlock()
				So(i, ShouldEqual, 2)
				i = 0

				f.Close()
				err = fs.Unmount()
				So(err, ShouldBeNil)
				So(fs.mounted, ShouldBeFalse)
			})
		})

		Convey("You can Mount() writable uncached", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessor,
				CacheData: false,
				Write:     true,
			}
			errm := fs.Mount(remoteConfig)
			So(errm, ShouldBeNil)
			defer fs.Unmount()

			Convey("You can Unmount()", func() {
				err := fs.Unmount()
				So(err, ShouldBeNil)
			})

			Convey("Creating files immediately uploads them", func() {
				sourceFile1 := filepath.Join(sourcePoint, "created1.file")
				_, err := os.Stat(sourceFile1)
				So(err, ShouldNotBeNil)
				sourceFile2 := filepath.Join(sourcePoint, "created2.file")
				_, err = os.Stat(sourceFile2)
				So(err, ShouldNotBeNil)

				f, err := os.OpenFile(filepath.Join(explicitMount, "created1.file"), os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)
				f.Close()
				defer os.Remove(sourceFile1)
				f, err = os.OpenFile(filepath.Join(explicitMount, "created2.file"), os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)
				f.Close()
				defer os.Remove(sourceFile2)

				// they exist prior to unmount
				<-time.After(50 * time.Millisecond)
				_, err = os.Stat(sourceFile1)
				So(err, ShouldBeNil)
				_, err = os.Stat(sourceFile2)
				So(err, ShouldBeNil)
			})

			Convey("You can write data directly to the remote", func() {
				sourceFile := filepath.Join(sourcePoint, "stream.file")
				_, err := os.Stat(sourceFile)
				So(err, ShouldNotBeNil)

				mountFile := filepath.Join(explicitMount, "stream.file")
				f, err := os.OpenFile(mountFile, os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)
				defer os.Remove(sourceFile)

				info, err := os.Stat(sourceFile)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 0)

				f.WriteString("test\n")

				info, err = os.Stat(sourceFile)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 5)

				info, err = os.Stat(mountFile)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 5)

				f.WriteString("test2\n")

				info, err = os.Stat(sourceFile)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 11)

				info, err = os.Stat(mountFile)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 11)

				f.Close()
				err = fs.Unmount()
				So(err, ShouldBeNil)
			})

			Convey("You can't have 2 writeable remotes", func() {
				err := fs.Unmount()
				So(err, ShouldBeNil)
				err = fs.Mount(remoteConfig, remoteConfig)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldEqual, "You can't have more than one writeable remote")
			})
		})

		Convey("You can Mount() read-only to a non-existent sub-dir", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessorNonExistent,
				CacheData: false,
				Write:     false,
			}
			err := fs.Mount(remoteConfig)
			So(err, ShouldBeNil)
			defer fs.Unmount()

			Convey("Getting the contents of the dir works", func() {
				entries, err := ioutil.ReadDir(explicitMount)
				So(err, ShouldBeNil)
				So(len(entries), ShouldEqual, 0)
			})
		})

		Convey("You can Mount() writable cached to a non-existent sub-dir", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessorNonExistent,
				CacheData: true,
				Write:     true,
			}
			errm := fs.Mount(remoteConfig)
			So(errm, ShouldBeNil)
			defer fs.Unmount()
			defer os.RemoveAll(sourceSubDir)

			Convey("You can Unmount()", func() {
				err := fs.Unmount()
				So(err, ShouldBeNil)
				So(checkEmpty(cacheBase), ShouldBeTrue)
			})

			Convey("Getting the contents of the dir works", func() {
				entries, err := ioutil.ReadDir(explicitMount)
				So(err, ShouldBeNil)
				So(len(entries), ShouldEqual, 0)
			})

			Convey("Unmounting after creating a file uploads it", func() {
				sourceFile1 := filepath.Join(sourceSubDir, "created1.file")
				_, err := os.Stat(sourceFile1)
				So(err, ShouldNotBeNil)

				f, err := os.OpenFile(filepath.Join(explicitMount, "created1.file"), os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)
				f.Close()
				defer os.Remove(sourceFile1)

				// doesn't exist prior to unmount
				_, err = os.Stat(sourceFile1)
				So(err, ShouldNotBeNil)

				err = fs.Unmount()
				So(err, ShouldBeNil)

				// does exist afterwards
				_, err = os.Stat(sourceFile1)
				So(err, ShouldBeNil)
			})
		})

		Convey("You can Mount() writable uncached to a non-existent sub-dir", func() {
			remoteConfig := &RemoteConfig{
				Accessor:  accessorNonExistent,
				CacheData: false,
				Write:     true,
			}
			errm := fs.Mount(remoteConfig)
			So(errm, ShouldBeNil)
			defer fs.Unmount()
			defer os.RemoveAll(sourceSubDir)

			Convey("You can Unmount()", func() {
				err := fs.Unmount()
				So(err, ShouldBeNil)
			})

			Convey("Getting the contents of the dir works", func() {
				entries, err := ioutil.ReadDir(explicitMount)
				So(err, ShouldBeNil)
				So(len(entries), ShouldEqual, 0)
			})

			Convey("Creating a file immediately uploads it", func() {
				sourceFile1 := filepath.Join(sourceSubDir, "created1.file")
				_, err := os.Stat(sourceFile1)
				So(err, ShouldNotBeNil)

				f, err := os.OpenFile(filepath.Join(explicitMount, "created1.file"), os.O_RDWR|os.O_CREATE, 0666)
				So(err, ShouldBeNil)
				f.Close()
				defer os.Remove(sourceFile1)

				// exists prior to unmount
				<-time.After(50 * time.Millisecond)
				_, err = os.Stat(sourceFile1)
				So(err, ShouldBeNil)
			})
		})

		Convey("You can Mount() read-only with a permanent cache", func() {
			remoteConfig := &RemoteConfig{
				Accessor: accessor,
				CacheDir: cachePermanent,
				Write:    false,
			}
			err := fs.Mount(remoteConfig)
			So(err, ShouldBeNil)
			defer fs.Unmount()

			Convey("You can Unmount()", func() {
				err := fs.Unmount()
				So(err, ShouldBeNil)
				So(checkEmpty(cachePermanent), ShouldBeTrue)
			})

			Convey("Unmount() after reading files does not delete the cached files", func() {
				data, err := ioutil.ReadFile(filepath.Join(explicitMount, "read.file"))
				So(err, ShouldBeNil)
				So(string(data), ShouldEqual, "test1\ntest2\n")
				err = fs.Unmount()
				So(err, ShouldBeNil)
				So(checkEmpty(cacheBase), ShouldBeTrue)
				So(checkEmpty(cachePermanent), ShouldBeFalse)

				Convey("Remounting and re-reading reads from the cached file", func() {
					// hack the cached file so we know we read from it and not
					// source; currently cache files are only validated based on
					// size
					cf := filepath.Join(cachePermanent, sourcePoint, "read.file")
					err = ioutil.WriteFile(cf, []byte("test1\ntestX\n"), 0644)
					So(err, ShouldBeNil)

					err = fs.Mount(remoteConfig)
					So(err, ShouldBeNil)
					data, err := ioutil.ReadFile(filepath.Join(explicitMount, "read.file"))
					So(err, ShouldBeNil)
					So(string(data), ShouldEqual, "test1\ntestX\n")
				})
			})
		})

		Convey("You must supply at least one RemoteConfig to Mount()", func() {
			err := fs.Mount()
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, "At least one RemoteConfig must be supplied")
		})

		Convey("You can't Mount() with a bad CacheDir", func() {
			remoteConfig := &RemoteConfig{
				Accessor: accessor,
				CacheDir: "/!",
			}
			err := fs.Mount(remoteConfig)
			So(err, ShouldNotBeNil)
		})

		Convey("UnmountOnDeath does nothing prior to mounting", func() {
			So(fs.handlingSignals, ShouldBeFalse)
			fs.UnmountOnDeath()
			So(fs.handlingSignals, ShouldBeFalse)
		})
	})

	Convey("You can make a New MuxFys with a default Mount", t, func() {
		defaultMnt := filepath.Join(tmpdir, "mnt")
		fs, err := New(&Config{})
		So(err, ShouldBeNil)
		So(fs.mountPoint, ShouldEqual, defaultMnt)
		_, err = os.Stat(defaultMnt)
		So(err, ShouldBeNil)
	})

	Convey("You can make a New MuxFys with an explicit ~ Mount", t, func() {
		expectedMount := filepath.Join(user.HomeDir, ".muxfys_test_mount_dir")
		explicitMount := "~/.muxfys_test_mount_dir"
		cfg := &Config{
			Mount: explicitMount,
		}
		fs, err := New(cfg)
		defer os.RemoveAll(expectedMount)
		So(err, ShouldBeNil)
		So(fs.mountPoint, ShouldEqual, expectedMount)
		_, err = os.Stat(expectedMount)
		So(err, ShouldBeNil)

		Convey("This fails for invalid home dir specs", func() {
			explicitMount := "~.muxfys_test_mount_dir"
			cfg := &Config{
				Mount: explicitMount,
			}
			_, err := New(cfg)
			So(err, ShouldNotBeNil)
		})
	})

	if user.Name != "root" {
		Convey("You can't make a New MuxFys with Mount point in /", t, func() {
			explicitMount := "/.muxfys_test_mount_dir"
			cfg := &Config{
				Mount: explicitMount,
			}
			_, err := New(cfg)
			defer os.RemoveAll(explicitMount)
			So(err, ShouldNotBeNil)
		})
	}

	Convey("You can't make a New MuxFys using a file as a Mount", t, func() {
		explicitMount := filepath.Join(tmpdir, "mntfile")
		os.OpenFile(explicitMount, os.O_RDONLY|os.O_CREATE, 0666)
		cfg := &Config{
			Mount: explicitMount,
		}
		_, err := New(cfg)
		defer os.RemoveAll(explicitMount)
		So(err, ShouldNotBeNil)
	})

	Convey("You can't make a New MuxFys using a Mount that already contains files", t, func() {
		explicitMount := filepath.Join(tmpdir, "mntfull")
		err := os.MkdirAll(explicitMount, os.FileMode(0777))
		So(err, ShouldBeNil)
		os.OpenFile(filepath.Join(explicitMount, "mntfile"), os.O_RDONLY|os.O_CREATE, 0666)
		cfg := &Config{
			Mount: explicitMount,
		}
		_, err = New(cfg)
		defer os.RemoveAll(explicitMount)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "was not empty")
	})
}

// checkEmpty checks if the given directory is empty.
func checkEmpty(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	return err == io.EOF
}
