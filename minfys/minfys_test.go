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
	"net/url"
	"os"
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

	/*
			For these tests to work, $WR_S3_TARGET must be the full URL to an
		    immediate child directory of a bucket that you have read and write
		    permissions for, eg:
			https://cog.domain.com/bucket/wr_tests

			The child directory must contain the following:
			perl -e 'for (1..100000) { printf "%06d\n", $_; }' > 100k.lines
			echo 1234567890abcdefghijklmnopqrstuvwxyz1234567890 > numalphanum.txt
			mkdir -p sub/deep
			touch sub/empty
			echo foo > sub/deep/bar
			export WR_BUCKET_SUB=s3://bucket/wr_tests
			s3cmd put 100k.lines $WR_BUCKET_SUB/100k.lines
			s3cmd put numalphanum.txt $WR_BUCKET_SUB/numalphanum.txt
			s3cmd put sub/empty $WR_BUCKET_SUB/sub/empty
			s3cmd put sub/deep/bar $WR_BUCKET_SUB/sub/deep/bar
			rm -fr 100k.lines numalphanum.txt sub
	*/

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

		cfg := Config{
			Target:     target,
			MountPoint: mountPoint,
			CacheDir:   cacheDir,
			AccessKey:  os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretKey:  os.Getenv("AWS_SECRET_ACCESS_KEY"),
			FileMode:   os.FileMode(0644),
			DirMode:    os.FileMode(0755),
			ReadOnly:   true,
			CacheData:  false,
			Debug:      false,
		}

		Convey("You can mount without local file caching", t, func() {
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
				rootEntries := []string{"100k.lines:file:700000", "numalphanum.txt:file:47", "sub:dir"}
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
				So(details, ShouldResemble, []string{"deep:dir", "empty:file:0"})

				entries, err = ioutil.ReadDir(mountPoint + "/sub/deep")
				So(err, ShouldBeNil)

				details = dirDetails(entries)
				So(details, ShouldResemble, []string{"bar:file:4"})
			})

			Convey("You can immediately list a subdir", func() {
				entries, err := ioutil.ReadDir(mountPoint + "/sub")
				So(err, ShouldBeNil)

				details := dirDetails(entries)
				So(details, ShouldResemble, []string{"deep:dir", "empty:file:0"})
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
		})

		Convey("You can mount with local file caching", t, func() {
			cfg.CacheData = true
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
		})

		Convey("You can mount the bucket directly", t, func() {
			u, err := url.Parse(target)
			parts := strings.Split(u.Path[1:], "/")
			cfg.Target = u.Scheme + "://" + u.Host + "/" + parts[0]
			cfg.CacheData = false
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
				cfg.CacheData = false
				cfg.Target = "https://cog.sanger.ac.uk/npg-repository"
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
				cfg.CacheData = false
				cfg.Target = "https://cog.sanger.ac.uk/npg-repository/references/Homo_sapiens/GRCh38_full_analysis_set_plus_decoy_hla/all/fasta"
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
				cfg.CacheData = false
				cfg.Target = "https://cog.sanger.ac.uk/npg-repository"
				cfg.AccessKey = ""
				cfg.SecretKey = ""
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
