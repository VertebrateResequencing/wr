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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	uploadFailureCacheMountPoint  = "mnt"
	uploadFailureCacheTargetPath  = "bucket/pfx"
	uploadFailureCacheOutputName  = "out.txt"
	uploadFailureCacheOutputText  = "hello"
	uploadFailureCacheUploadedKey = "/" + uploadFailureCacheTargetPath + "/" + uploadFailureCacheOutputName

	// uploadFailureCacheProfile is the S3 config section this test's mounts
	// name, chosen so that no config file of a real user's could carry it; see
	// useFakeS3Config for why the name is what keeps the test off the network.
	uploadFailureCacheProfile = "wr-test-fake-s3"
)

// fakeS3 stands in for the remote a writable cached mount uploads to at unmount,
// so that a test can decide whether that upload succeeds without needing a real
// S3 endpoint or the network. It answers a PUT with either the rejection that
// makes muxfys keep its cache, or an acceptance that records the body, and
// answers everything else - the listings muxfys makes while mounting - with an
// empty bucket.
type fakeS3 struct {
	sync.Mutex
	uploads       map[string]string
	rejectUploads bool
	srv           *httptest.Server
}

func newFakeS3(rejectUploads bool) *fakeS3 {
	remote := &fakeS3{uploads: make(map[string]string), rejectUploads: rejectUploads}
	remote.srv = httptest.NewServer(http.HandlerFunc(remote.serve))

	return remote
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		f.recordUpload(r)

		if f.rejectUploads {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+
				`<Error><Code>AccessDenied</Code><Message>no</Message></Error>`)

			return
		}

		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		w.WriteHeader(http.StatusOK)

		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+
		`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>bucket</Name>`+
		`<Prefix></Prefix><KeyCount>0</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`+
		`</ListBucketResult>`)
}

func (f *fakeS3) recordUpload(r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}

	f.Lock()
	defer f.Unlock()

	f.uploads[r.URL.Path] = string(body)
}

// uploaded returns the body of the last PUT made to the given path.
func (f *fakeS3) uploaded(path string) string {
	f.Lock()
	defer f.Unlock()

	return f.uploads[path]
}

// host is what an s3 config file's host_base must be for muxfys to talk to this
// server instead of a real S3.
func (f *fakeS3) host() string {
	return strings.TrimPrefix(f.srv.URL, "http://")
}

func TestUploadFailureCacheDeletion(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("When a job's output fails to upload, the cache muxfys keeps is deleted", t, func() {
		remote := newFakeS3(true)
		defer remote.srv.Close()

		useFakeS3Config(t, remote)

		client := newLiveExecuteCaptureClient(&liveTouchCapture{})
		job := cachedWriteMountJob(client, mountTestCwd(t))

		err := client.Execute(context.Background(), job, "/bin/sh")
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "failed to upload")
		So(job.ActualCwd, ShouldNotBeBlank)

		So(muxfysCacheNamesIn(filepath.Dir(job.ActualCwd)), ShouldBeEmpty)
	})

	Convey("A successful job's cache survives cleanup, so its output reaches the remote", t, func() {
		remote := newFakeS3(false)
		defer remote.srv.Close()

		useFakeS3Config(t, remote)

		client := newLiveExecuteCaptureClient(&liveTouchCapture{})
		job := cachedWriteMountJob(client, mountTestCwd(t))

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)
		So(job.ActualCwd, ShouldNotBeBlank)

		So(remote.uploaded(uploadFailureCacheUploadedKey), ShouldContainSubstring, uploadFailureCacheOutputText)
		So(muxfysCacheNamesIn(filepath.Dir(job.ActualCwd)), ShouldBeEmpty)
	})
}

// cachedWriteMountJob returns a job that writes a file inside a writable cached
// mount and then cleans up after itself, which is the arrangement that gives
// muxfys a cache of its own choosing inside the workspace wr makes: the job is
// not CwdMatters, so its CacheBase defaults to that workspace.
//
// One mount retry keeps a mount that cannot be made from holding the test up.
//
// The Target names the fake's own S3 profile, which is what wr hands
// muxfys.S3ConfigFromEnvironment (buildRemoteConfigs), and so what decides
// which config section the endpoint comes from; see useFakeS3Config.
func cachedWriteMountJob(client *Client, cwd string) *Job {
	job := liveExecuteHashedCwdJob(client, cwd,
		"echo "+uploadFailureCacheOutputText+" > "+
			uploadFailureCacheMountPoint+"/"+uploadFailureCacheOutputName)
	job.MountConfigs = MountConfigs{{
		Mount:   uploadFailureCacheMountPoint,
		Retries: 1,
		Targets: []MountTarget{{
			Profile: uploadFailureCacheProfile,
			Path:    uploadFailureCacheTargetPath,
			Cache:   true,
			Write:   true,
		}},
	}}
	job.Behaviours = Behaviours{{When: OnExit, Do: CleanupAll}}

	return job
}

// mountTestCwd makes the directory a mounting job's Cwd is, outside t.TempDir()
// so that a mount left behind by a killed run can still be reaped, and fails the
// test if any mount made below it outlives the test.
func mountTestCwd(t *testing.T) string {
	t.Helper()

	dir, err := newTestTempDir("mounts")
	So(err, ShouldBeNil)

	failMountTestOnTimeout(t, dir)

	t.Cleanup(func() {
		if forced := releaseMuxFysMountsUnder(dir); len(forced) > 0 {
			t.Errorf("mounts outlived the test that made them: %v", forced)
		}

		os.RemoveAll(dir)
	})

	cwd := filepath.Join(dir, "cwd")
	So(os.MkdirAll(cwd, 0o755), ShouldBeNil)

	return cwd
}

// muxfysCacheNamesIn is what a muxfys-chosen cache directory looks like from
// outside: an entry of the given directory whose name starts with the prefix
// muxfys gives the caches it names for itself.
func muxfysCacheNamesIn(dir string) []string {
	var found []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), muxfysCachePrefix) {
			found = append(found, entry.Name())
		}
	}

	return found
}

// useFakeS3Config points muxfys.S3ConfigFromEnvironment at the given fake S3 for
// the rest of the test, by writing an s3 config that names it under this test's
// own profile section and putting that file in the environment.
//
// The environment variables have to be set because they are where muxfys reads a
// s3cmd-style config from, but they are not on their own enough to keep the test
// off the network. muxfys ini.LooseLoads ~/.s3cfg, $AWS_SHARED_CREDENTIALS_FILE,
// ~/.aws/credentials, $AWS_CONFIG_FILE and then ~/.aws/config, MERGING them with
// the later files winning - so both real files come after the ones named here,
// and a [default] host_base in the ~/.aws/config of whoever runs the tests would
// override this fake and aim the mount at their real endpoint. Any upload
// failure reaches the branch under test, so the failing direction could then
// pass for the wrong reason while really talking to S3.
//
// The section name is what closes that: a profile no real config file carries
// cannot be merged into, and muxfys errors rather than falling back when a NAMED
// profile's section is missing, so the test either reaches this server or fails
// loudly.
func useFakeS3Config(t *testing.T, remote *fakeS3) {
	t.Helper()

	config := filepath.Join(t.TempDir(), "s3cfg")
	content := "[" + uploadFailureCacheProfile + "]\naccess_key = k\nsecret_key = s\nhost_base = " +
		remote.host() + "\nuse_https = False\nregion = us-east-1\n"
	So(os.WriteFile(config, []byte(content), 0o600), ShouldBeNil)

	t.Setenv("AWS_CONFIG_FILE", config)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", config)
	t.Setenv("AWS_ACCESS_KEY_ID", "k")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "s")
	t.Setenv("AWS_DEFAULT_REGION", "us-east-1")
}
