/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package jobqueue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// uploadHostileUmask clears nothing, so the mode an uploaded file's directories
// end up with is entirely the mode the code asked for. Under it, os.ModePerm
// made them world writable, and any other local user could then delete or
// replace the 0600 file inside, since unlinking needs write permission on the
// directory rather than on the file.
const uploadHostileUmask = 0

// uploadOwnerOnlyPerm is the mode a directory holding an uploaded file has to
// have. It is written out rather than taken from ownerOnlyDir on purpose: a
// test that compares the code's constant with itself passes whatever that
// constant is changed to, which is exactly the mistake this file exists to
// catch.
const uploadOwnerOnlyPerm os.FileMode = 0o700

// uploadOwnerOnlyFilePerm is the mode an uploaded file has to have, written out
// for the same reason as uploadOwnerOnlyPerm.
const uploadOwnerOnlyFilePerm os.FileMode = 0o600

// uploadWorldReadablePerm is the mode of a file a caller-named upload replaces,
// wider than the upload may leave it.
const uploadWorldReadablePerm os.FileMode = 0o644

func TestServerUploadFile(t *testing.T) {
	ctx := context.Background()

	Convey("Given a server with an upload directory", t, func() {
		s := &Server{uploadDir: t.TempDir()}

		Convey("Uploading to a caller-named path that holds a longer file replaces its content", func() {
			savePath := filepath.Join(t.TempDir(), "config.yml")
			old := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"
			err := os.WriteFile(savePath, []byte(old), ownerReadWrite)
			So(err, ShouldBeNil)

			newContent := "short\n"
			returned, err := s.uploadFile(ctx, strings.NewReader(newContent), savePath)

			content, errr := os.ReadFile(savePath)
			So(errr, ShouldBeNil)
			So(string(content), ShouldEqual, newContent)
			So(returned, ShouldEqual, savePath)
			So(err, ShouldBeNil)
		})

		Convey("Uploading to a caller-named path that holds a shorter file replaces its content", func() {
			savePath := filepath.Join(t.TempDir(), "config.yml")
			err := os.WriteFile(savePath, []byte("tiny\n"), ownerReadWrite)
			So(err, ShouldBeNil)

			newContent := "a much longer replacement payload\n"
			returned, err := s.uploadFile(ctx, strings.NewReader(newContent), savePath)

			content, errr := os.ReadFile(savePath)
			So(errr, ShouldBeNil)
			So(string(content), ShouldEqual, newContent)
			So(returned, ShouldEqual, savePath)
			So(err, ShouldBeNil)
		})

		Convey("Uploading to a caller-named path that holds a world-readable file leaves it owner-only", func() {
			savePath := filepath.Join(t.TempDir(), "credentials")
			err := os.WriteFile(savePath, []byte("old\n"), uploadWorldReadablePerm)
			So(err, ShouldBeNil)

			// set explicitly, since WriteFile's mode is filtered by the umask
			err = os.Chmod(savePath, uploadWorldReadablePerm)
			So(err, ShouldBeNil)

			newContent := "aws_secret_access_key = secret\n"
			returned, err := s.uploadFile(ctx, strings.NewReader(newContent), savePath)
			So(err, ShouldBeNil)
			So(returned, ShouldEqual, savePath)

			info, err := os.Stat(savePath)
			So(err, ShouldBeNil)
			So(info.Mode().Perm(), ShouldEqual, uploadOwnerOnlyFilePerm)

			content, err := os.ReadFile(savePath)
			So(err, ShouldBeNil)
			So(string(content), ShouldEqual, newContent)
		})

		Convey("Uploading with an empty savePath stores the data at an md5-based path", func() {
			content := "md5 named content\n"

			first, err := s.uploadFile(ctx, strings.NewReader(content), "")
			So(err, ShouldBeNil)
			So(first, ShouldStartWith, s.uploadDir)

			stored, errr := os.ReadFile(first)
			So(errr, ShouldBeNil)
			So(string(stored), ShouldEqual, content)

			Convey("And uploading identical content again generates no error and reuses the path", func() {
				second, errs := s.uploadFile(ctx, strings.NewReader(content), "")

				stored, errr := os.ReadFile(second)
				So(errr, ShouldBeNil)
				So(string(stored), ShouldEqual, content)
				So(second, ShouldEqual, first)
				So(errs, ShouldBeNil)

				entries, errg := filepath.Glob(filepath.Join(s.uploadDir, "file_upload*"))
				So(errg, ShouldBeNil)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

// TestServerUploadDirsAreOwnerOnly proves uploadFile keeps the promise its doc
// comment makes - that what it stores is the server owner's alone - for the
// directories it creates as well as for the file it writes. An uploaded file is
// typically one of the `wr add --cloud_config_files` the manager copies to
// every cloud server it spawns, which by default are the submitter's ~/.s3cfg,
// ~/.aws/credentials and ~/.aws/config, so being able to replace one is being
// able to choose the cloud credentials those servers use.
func TestServerUploadDirsAreOwnerOnly(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server whose upload directory does not exist yet", t, func() {
		// the temp dir has to exist before the umask changes, or the modes
		// asserted on would be inherited from a parent made under the same
		// hostile umask rather than chosen by the code.
		base := t.TempDir()
		s := &Server{uploadDir: filepath.Join(base, "uploads")}

		Convey("The directories made for an md5-named upload are all owner-only", func() {
			stored := uploadUnderUmask(ctx, s, "", "md5 named content\n")

			So(stored, ShouldStartWith, s.uploadDir)
			assertOwnerOnlyDirsBelow(base, stored)
		})

		Convey("The directories made for a caller-named upload are all owner-only", func() {
			savePath := filepath.Join(base, "deep", "deeper", "config.yml")

			stored := uploadUnderUmask(ctx, s, savePath, "caller named content\n")

			So(stored, ShouldEqual, savePath)
			assertOwnerOnlyDirsBelow(base, stored)
		})
	})
}

// uploadUnderUmask uploads content to savePath with the process umask set to
// uploadHostileUmask, returning the path it was stored at.
//
// syscall.Umask is process-wide and every test in a package shares one process,
// so the umask is restored as the helper returns (deferred, so a failed
// assertion inside cannot leak it) rather than at the end of the test. No test
// in this package calls t.Parallel(), so no other test body can run inside this
// window, but background goroutines outliving earlier tests could still create
// files in it, and one call is as narrow as the window gets.
func uploadUnderUmask(ctx context.Context, s *Server, savePath, content string) string {
	previous := syscall.Umask(uploadHostileUmask)
	defer syscall.Umask(previous)

	stored, err := s.uploadFile(ctx, strings.NewReader(content), savePath)
	So(err, ShouldBeNil)

	return stored
}

// assertOwnerOnlyDirsBelow asserts that every directory between base
// (exclusive) and path (exclusive) grants nothing to group or other.
func assertOwnerOnlyDirsBelow(base, path string) {
	checked := 0

	for dir := filepath.Dir(path); dir != base; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		So(err, ShouldBeNil)
		So(info.Mode().Perm(), ShouldEqual, uploadOwnerOnlyPerm)

		checked++
	}

	So(checked, ShouldBeGreaterThan, 0)
}
