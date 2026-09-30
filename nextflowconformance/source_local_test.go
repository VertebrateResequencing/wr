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

package nextflowconformance

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	nextflowLocalHashFailure       = "E_RUNTIME_HASH"
	nextflowLocalMissingFailure    = "E_RUNTIME_MISSING"
	nextflowLocalInputFailure      = "E_INPUT"
	nextflowLocalCandidateMutation = "candidate"
)

type nextflowLocalTransport struct {
	upstream http.RoundTripper
	local    atomic.Int32
}

func nextflowLocalFixture(t *testing.T) (*acquisitionFixture, []artefact, *nextflowLocalTransport) {
	t.Helper()
	f := newAcquisitionFixture(t)
	locals := make([]artefact, 0, 2)

	for _, role := range []string{roleEnvironment, roleJava} {
		data := []byte("Inert " + role + " snapshot fixture; never executed.\n")
		relative := "local-snapshots/" + role + "/fixture"
		name := filepath.Join(f.cache, relative)
		So(os.MkdirAll(filepath.Dir(name), 0700), ShouldBeNil)
		So(os.WriteFile(name, data, 0600), ShouldBeNil)
		So(os.Chmod(name, 0751), ShouldBeNil)

		locals = append(locals, artefact{ID: "LOCAL_" + strings.ToUpper(strings.ReplaceAll(role, "-", "_")),
			Role: role, Origin: localOriginPrefix + testHash(data), Packaging: packFile,
			File: fileRef{Path: relative, SHA256: testHash(data), Bytes: int64(len(data))}, Dependencies: []string{}})
	}

	for index := range f.lock.Artefacts {
		if f.lock.Artefacts[index].Role == roleRuntime {
			f.lock.Artefacts[index].Dependencies = []string{locals[0].ID, locals[1].ID}
		}
	}

	f.lock.Artefacts = append(slices.Clone(locals), f.lock.Artefacts...)
	f.save(t)
	transport := &nextflowLocalTransport{upstream: f.server.Client().Transport}
	f.server.Client().Transport = transport

	return f, locals, transport
}

func (transport *nextflowLocalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != httpsRule {
		transport.local.Add(1)
	}

	return transport.upstream.RoundTrip(request)
}

func TestNextflowLocalSnapshotReuse(t *testing.T) {
	Convey("Locked acquisition retains both inert local snapshots and runtime edges", t, func() {
		f, locals, transport := nextflowLocalFixture(t)
		before := nextflowSnapshot(t, filepath.Join(f.cache, "local-snapshots"))

		original := make([]os.FileInfo, len(locals))
		for index, item := range locals {
			info, err := os.Stat(filepath.Join(f.cache, item.File.Path))
			So(err, ShouldBeNil)

			original[index] = info
		}

		code, output := f.run(t, commandAcquire)
		So(code, ShouldEqual, 0)
		So(output.Complete, ShouldBeTrue)
		So(output.Counts.VerifiedArtifacts, ShouldEqual, 5)
		So(f.requests.Load(), ShouldEqual, 3)
		So(transport.local.Load(), ShouldEqual, 0)
		f.accept(t)
		So(f.lock.Artefacts[:2], ShouldResemble, locals)

		for index, item := range locals {
			name := filepath.Join(f.cache, item.File.Path)
			data, err := os.ReadFile(name)
			So(err, ShouldBeNil)
			info, err := os.Stat(name)
			So(err, ShouldBeNil)
			So(os.SameFile(original[index], info), ShouldBeTrue)
			So(testHash(data), ShouldEqual, item.File.SHA256)
			So(int64(len(data)), ShouldEqual, item.File.Bytes)
			So(info.Mode().Perm(), ShouldEqual, 0751)

			resolved, err := filepath.EvalSymlinks(name)
			So(err, ShouldBeNil)
			t.Logf("Nextflow inert local provenance role=%s observed_source=%q resolved_target=%q snapshot=%q "+
				"origin=%s sha256=%s bytes=%d mode=%04o",
				item.Role, name, resolved, name, item.Origin, testHash(data), len(data), info.Mode().Perm())
		}

		So(nextflowSnapshot(t, filepath.Join(f.cache, "local-snapshots")), ShouldResemble, before)
		runtimeIndex := slices.IndexFunc(f.lock.Artefacts, func(item artefact) bool { return item.Role == roleRuntime })
		So(f.lock.Artefacts[runtimeIndex].Dependencies, ShouldResemble, []string{locals[0].ID, locals[1].ID})
		f.server.Close()
		code, output = f.run(t, commandValidate)
		So(code, ShouldEqual, 0)
		So(output.Complete, ShouldBeTrue)
		So(f.requests.Load(), ShouldEqual, 3)
		So(transport.local.Load(), ShouldEqual, 0)
	})
}

func TestNextflowLocalSnapshotFailures(t *testing.T) {
	Convey("Local snapshot failures preserve reviewed inputs, candidate, and cache", t, func() {
		for _, role := range []string{roleJava, roleEnvironment} {
			Convey(role, func() {
				for _, mutation := range []string{"missing-file", "content", "length", "directory", "final-symlink", "ancestor",
					"escape", "absolute-file", "digest", "cache-symlink", "cache-ancestor", nextflowLocalCandidateMutation} {
					Convey(mutation, func() {
						f, locals, transport := nextflowLocalFixture(t)
						index := slices.IndexFunc(f.lock.Artefacts, func(item artefact) bool { return item.Role == role })
						item := &f.lock.Artefacts[index]
						candidate := filepath.Join(f.cache, "sources.lock.candidate.json")
						So(os.WriteFile(candidate, []byte("previous candidate\n"), 0600), ShouldBeNil)

						cacheRoot := f.cache
						expected := mutateNextflowLocalSnapshot(t, f, item, mutation)
						candidate = filepath.Join(f.cache, "sources.lock.candidate.json")
						f.save(t)
						cacheBefore := nextflowSnapshot(t, cacheRoot)
						corpusBefore := nextflowSnapshot(t, f.root)

						outsideBefore := nextflowSnapshot(t, filepath.Dir(cacheRoot))

						if mutation == nextflowLocalCandidateMutation {
							candidate = filepath.Join(f.cache, locals[index].File.Path)
						}

						code, output := f.runContext(t, context.Background(), commandAcquire,
							acquisitionOptions{client: f.server.Client()}, "--lock-candidate", candidate)
						So(code, ShouldEqual, 2)
						So(output.Complete, ShouldBeFalse)
						So(output.Counts.VerifiedArtifacts, ShouldEqual, 0)
						So(output.Diagnostics[0].Code, ShouldEqual, expected)
						So(nextflowSnapshot(t, cacheRoot), ShouldResemble, cacheBefore)
						So(nextflowSnapshot(t, f.root), ShouldResemble, corpusBefore)
						So(nextflowSnapshot(t, filepath.Dir(cacheRoot)), ShouldResemble, outsideBefore)
						So(transport.local.Load(), ShouldEqual, 0)
						So(f.requests.Load(), ShouldEqual, 0)
						t.Logf("Nextflow local failure role=%s mutation=%s code=%s https_requests=%d local_requests=%d",
							role, mutation, expected, f.requests.Load(), transport.local.Load())
					})
				}
			})
		}
	})
}

func mutateNextflowLocalSnapshot(t *testing.T, f *acquisitionFixture, item *artefact, mutation string) string {
	t.Helper()

	name := filepath.Join(f.cache, item.File.Path)

	switch mutation {
	case "missing-file":
		So(os.Remove(name), ShouldBeNil)

		return nextflowLocalMissingFailure
	case "content":
		So(os.WriteFile(name, []byte(strings.Repeat("x", int(item.File.Bytes))), 0600), ShouldBeNil)

		return nextflowLocalHashFailure
	case "length":
		So(os.WriteFile(name, []byte("short"), 0600), ShouldBeNil)

		return nextflowLocalHashFailure
	case "directory":
		So(os.Remove(name), ShouldBeNil)
		So(os.Mkdir(name, 0751), ShouldBeNil)
	case "final-symlink":
		target := filepath.Join(t.TempDir(), "target")
		So(os.Rename(name, target), ShouldBeNil)
		So(os.Symlink(target, name), ShouldBeNil)
	case "ancestor":
		target := filepath.Join(t.TempDir(), "target")
		So(os.Rename(filepath.Dir(name), target), ShouldBeNil)
		So(os.Symlink(target, filepath.Dir(name)), ShouldBeNil)
	case "escape":
		item.File.Path = "../outside"

		return nextflowLocalInputFailure
	case "absolute-file":
		item.File.Path = name

		return nextflowLocalInputFailure
	case "digest":
		item.Origin = localOriginPrefix + strings.Repeat("0", 64)

		return nextflowLocalInputFailure
	case "cache-symlink":
		target := filepath.Join(t.TempDir(), "cache-link")
		So(os.Symlink(f.cache, target), ShouldBeNil)
		f.cache = target
	case "cache-ancestor":
		target := filepath.Join(t.TempDir(), "parent-link")
		So(os.Symlink(filepath.Dir(f.cache), target), ShouldBeNil)
		f.cache = filepath.Join(target, filepath.Base(f.cache))
	}

	return "E_SOURCE_PATH"
}
