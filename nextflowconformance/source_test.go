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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // The pinned Git repository uses SHA-1 object IDs.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const nextflowFixtureSource = "docs/reference/syntax.md"

const nextflowTargetIdentityCode = "E_TARGET_IDENTITY"

type acquisitionFixture struct {
	root, cache string
	lock        *lockRecord
	blobs       map[string][]byte
	server      *httptest.Server
	requests    atomic.Int32
	fail        atomic.Int32
}

func newAcquisitionFixture(t *testing.T) *acquisitionFixture {
	t.Helper()
	fixture := &acquisitionFixture{root: t.TempDir(), cache: t.TempDir(), blobs: map[string][]byte{}}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.requests.Add(1)

		if fixture.fail.Load() == 1 || fixture.fail.Load() == 2 && r.URL.Path == "/SOURCE" {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		data, exists := fixture.blobs[r.URL.Path]
		if !exists {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		if _, err := w.Write(data); err != nil {
			return
		}
	}))
	t.Cleanup(fixture.server.Close)
	fixture.blobs["/SOURCE"] = nextflowFixtureInput(t, "nextflow-source.tar.gz")
	fixture.blobs["/RUNTIME_NEXTFLOW"] = nextflowFixtureInput(t, "nextflow-26.04.6-dist")
	fixture.blobs["/POM"] = []byte("<project><groupId>org.example</groupId><artifactId>nextflow-test</artifactId>" +
		"<version>1</version></project>\n")
	So(testHash(fixture.blobs["/RUNTIME_NEXTFLOW"]), ShouldEqual, distributionHash)
	So(len(fixture.blobs["/RUNTIME_NEXTFLOW"]), ShouldEqual, distributionBytes)
	fixture.lock = fixture.nextflowLock(t)
	fixture.writeCorpus(t)
	So(os.MkdirAll(filepath.Join(fixture.cache, javaDirectory), 0700), ShouldBeNil)
	So(os.WriteFile(filepath.Join(fixture.cache, javaDirectory, "release"),
		[]byte("JAVA_VERSION=21\n"), 0600), ShouldBeNil)
	fixture.save(t)

	return fixture
}

func nextflowPinnedFixture(t *testing.T) (*acquisitionFixture, acquisitionOptions, string) {
	t.Helper()
	f := newAcquisitionFixture(t)
	So(os.Remove(filepath.Join(f.root, "sources.lock.json")), ShouldBeNil)
	f.blobs["/releases/tags/v26.04.6"] = []byte(`{"tag_name":"v26.04.6"}`)
	f.blobs["/git/tags/"+pinnedTag] = []byte(`{"sha":"` + pinnedTag + `","object":{"sha":"` +
		pinnedCommit + `","type":"commit"}}`)
	f.blobs["/git/commits/"+pinnedCommit] = []byte(`{"sha":"` + pinnedCommit + `","tree":{"sha":"` + pinnedTree + `"}}`)
	f.blobs["/git/trees/"+pinnedTree] = nextflowFixtureInput(t, "nextflow-tree.json")
	f.blobs["/nextflow-io/nextflow/tar.gz/"+pinnedCommit] = f.blobs["/SOURCE"]
	f.blobs["/nextflow-io/nextflow/releases/download/v26.04.6/nextflow-26.04.6-dist"] = f.blobs["/RUNTIME_NEXTFLOW"]

	for _, object := range []struct{ name, path, hash string }{
		{"nextflow", "/nextflow-io/nextflow/releases/download/v26.04.6/nextflow", launcherHash},
		{"antlr4.pom", "/maven2/me/sunlan/antlr4/4.13.2.6/antlr4-4.13.2.6.pom",
			"1ba04a4da8e5a2acf0ac6eece4df82280bcd6a91f44707bb0b4b9c8d73f8da32"},
		{"groovy.pom", "/maven2/org/apache/groovy/groovy/4.0.31/groovy-4.0.31.pom",
			"ef7fc8e69aeeb2c1d4ed4df1bcdb5fd82ecfacf9fd7574bee0f89b9984d74bda"},
		{"pf4j.pom", "/maven2/org/pf4j/pf4j/3.14.1/pf4j-3.14.1.pom",
			"95c5843942717af5cdfe5188b98dec0034439e80978be0ef67637ea684154818"},
	} {
		data, err := os.ReadFile(filepath.Join("../.tmp/agent/runtime-packaging", object.name))
		So(err, ShouldBeNil)
		So(testHash(data), ShouldEqual, object.hash)
		f.blobs[object.path] = data
	}

	destination, err := url.Parse(f.server.URL)
	So(err, ShouldBeNil)

	f.server.Client().Transport = nextflowPinnedTransport{upstream: f.server.Client().Transport, destination: destination}
	home, err := filepath.Abs("../.tmp/agent/java21/jdk-21.0.12.1+1")
	So(err, ShouldBeNil)

	return f, acquisitionOptions{client: f.server.Client(), baseURL: f.server.URL}, home
}

func (f *acquisitionFixture) nextflowLock(t *testing.T) *lockRecord {
	t.Helper()

	var tree struct {
		SHA       string      `json:"sha"`
		Truncated bool        `json:"truncated"`
		Entries   []treeEntry `json:"tree"`
	}
	So(json.Unmarshal(nextflowFixtureInput(t, "nextflow-tree.json"), &tree), ShouldBeNil)
	So(tree.SHA, ShouldEqual, pinnedTree)
	files := nextflowFixtureFiles(t, f.blobs["/SOURCE"], tree.Entries)

	lock := &lockRecord{
		Schema: 1, ID: "NEXTFLOW_FIXTURE", Origin: f.server.URL, Revision: pinnedCommit,
		Tree:  treeRecord{SHA: tree.SHA, Origin: f.server.URL + "/tree", Truncated: tree.Truncated, Entries: tree.Entries},
		Files: files, Artefacts: []artefact{},
		Environment: environment{
			JavaHome: javaDirectory, JavaVersion: "Independent inert Java tree fixture; no Java executed",
			OS: "linux", Arch: "amd64", Variables: []envValue{},
			JavaFiles: []javaFile{{File: fileRef{Path: "release", SHA256: testHash([]byte("JAVA_VERSION=21\n")),
				Bytes: int64(len("JAVA_VERSION=21\n"))}, Mode: 0600}},
			Dependencies: []coordinate{{ID: "NEXTFLOW_TEST", Group: "org.example",
				Name: "nextflow-test", Version: "1", POMID: "POM"}},
		},
	}
	for _, item := range []struct{ id, role, packaging, name string }{
		{"POM", roleMetadata, packFile, "nextflow-test.pom"},
		{"RUNTIME_NEXTFLOW", roleRuntime, packOpaque, "nextflow-26.04.6-dist"},
		{"SOURCE", roleSource, packExtracted, "nextflow-source.tar.gz"},
	} {
		data := f.blobs["/"+item.id]

		acquired := artefact{ID: item.id, Role: item.role, Origin: f.server.URL + "/" + item.id,
			File:      fileRef{Path: item.name, SHA256: testHash(data), Bytes: int64(len(data))},
			Packaging: item.packaging, Dependencies: []string{}}
		if item.role == roleMetadata {
			acquired.Coordinate = new("org.example:nextflow-test:1")
		}

		lock.Artefacts = append(lock.Artefacts, acquired)
	}

	return lock
}

// These fixtures use the actual pinned bytes because the public lock contract
// rejects substitute release identities. No fixture fetches or runs Nextflow.
func nextflowFixtureInput(t *testing.T, name string) []byte {
	t.Helper()

	root := os.Getenv("NEXTFLOW_CONFORMANCE_TEST_INPUTS")
	if root == "" {
		root = "../.tmp/nextflow-conformance/test-inputs"
	}

	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		err = fmt.Errorf("Nextflow A1 fixture prerequisite; run phase1-acquisition-part1-provision.txt: %w", err)
	}

	So(err, ShouldBeNil)

	return data
}

func nextflowFixtureFiles(t *testing.T, data []byte, entries []treeEntry) []sourceFile {
	t.Helper()

	compressed, err := gzip.NewReader(bytes.NewReader(data))
	So(err, ShouldBeNil)

	defer compressed.Close()

	reader := tar.NewReader(compressed)
	contents := map[string][]byte{}

	var archiveErr error

	for {
		header, readErr := reader.Next()
		if readErr == io.EOF {
			break
		}

		if readErr != nil {
			archiveErr = readErr

			break
		}

		if header.Typeflag == tar.TypeDir || header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}

		_, name, _ := strings.Cut(header.Name, "/")

		content, contentErr := io.ReadAll(reader)
		if contentErr != nil {
			archiveErr = contentErr

			break
		}

		if header.Typeflag == tar.TypeSymlink {
			content = []byte(header.Linkname)
		}

		contents[name] = content
	}

	So(archiveErr, ShouldBeNil)

	files := make([]sourceFile, 0, len(contents))
	mismatches := 0

	for _, entry := range entries {
		if entry.Type == gitTreeKind {
			continue
		}

		content, exists := contents[entry.Path]
		if !exists || testGitHash("blob", content) != entry.SHA {
			mismatches++
		}

		state := "unreviewed"
		if entry.Path == nextflowFixtureSource {
			state = sourceSelected
		}

		files = append(files, sourceFile{Path: entry.Path, Blob: entry.SHA, Mode: entry.Mode,
			SHA256: testHash(content), Bytes: int64(len(content)), State: state})
	}

	So(mismatches, ShouldEqual, 0)
	So(len(files), ShouldEqual, len(contents))
	slices.SortFunc(files, func(a, b sourceFile) int { return strings.Compare(a.Path, b.Path) })

	return files
}

func testHash(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func (f *acquisitionFixture) writeCorpus(t *testing.T) {
	t.Helper()
	records := corpusFixtures(t)
	delete(records, recordsOf[*lockRecord](records)[0].ID)
	// Adapt the independent relational records to a real, selected source file.
	var source sourceFile

	for _, file := range f.lock.Files {
		if file.Path == nextflowFixtureSource {
			source = file
		}
	}

	block := recordsOf[*blockRecord](records)[0]
	block.Span = span{File: source.Path, Start: 0, End: source.Bytes, SHA256: source.SHA256}
	review := recordsOf[*reviewRecord](records)[0]
	review.SourceSpans = []span{block.Span}
	review.InputHashes.Source = []fileRef{{Path: source.Path, Bytes: source.Bytes, SHA256: source.SHA256}}
	recordsOf[*batchRecord](records)[0].SourceSpans = []span{block.Span}
	writeCorpus(t, f.root, records)
}

func (f *acquisitionFixture) save(t *testing.T) {
	t.Helper()

	data, err := json.MarshalIndent(f.lock, "", "  ")
	So(err, ShouldBeNil)
	So(os.WriteFile(filepath.Join(f.root, "sources.lock.json"), append(data, '\n'), 0600), ShouldBeNil)
}

func (f *acquisitionFixture) run(t *testing.T, command string) (int, result) {
	t.Helper()

	return f.runContext(t, context.Background(), command, acquisitionOptions{client: f.server.Client()})
}

func (f *acquisitionFixture) runContext(t *testing.T, ctx context.Context, command string,
	options acquisitionOptions, extraArgs ...string) (int, result) {
	t.Helper()

	var out, diagnostics bytes.Buffer

	args := append([]string{command, rootFlag, f.root, cacheFlag, f.cache}, extraArgs...)
	code := runCLI(ctx, args, &out, &diagnostics, options)

	var value result
	So(json.Unmarshal(out.Bytes(), &value), ShouldBeNil)
	t.Logf("Nextflow CLI exit=%d stdout=%s stderr=%s", code, out.String(), diagnostics.String())

	return code, value
}

func (f *acquisitionFixture) accept(t *testing.T) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(f.cache, "sources.lock.candidate.json"))
	So(err, ShouldBeNil)
	So(os.WriteFile(filepath.Join(f.root, "sources.lock.json"), data, 0600), ShouldBeNil)
	value, err := decodeRecord(kindLock, data)
	So(err, ShouldBeNil)

	var ok bool

	f.lock, ok = value.(*lockRecord)
	So(ok, ShouldBeTrue)
}

func (f *acquisitionFixture) badArchive(t *testing.T, mutation string) {
	t.Helper()

	header := &tar.Header{Name: "../nextflow-escape", Mode: 0644, Typeflag: tar.TypeReg}
	headers := []*tar.Header{header}

	switch mutation {
	case "absolute":
		header.Name = filepath.Join(filepath.Dir(f.cache), "nextflow-escape")
	case "duplicate":
		header.Name = "repository/duplicate"
		headers = append(headers, header)
	case "symlink":
		header.Name, header.Typeflag, header.Linkname = "repository/link", tar.TypeSymlink, "../../nextflow-escape"
	}

	data := testArchive(t, headers, make([][]byte, len(headers)))

	f.blobs["/SOURCE"] = data
	for i := range f.lock.Artefacts {
		if f.lock.Artefacts[i].Role == roleSource {
			f.lock.Artefacts[i].File.SHA256, f.lock.Artefacts[i].File.Bytes = testHash(data), int64(len(data))
		}
	}

	f.save(t)
}

func testArchive(t *testing.T, headers []*tar.Header, contents [][]byte) []byte {
	t.Helper()

	var data bytes.Buffer

	compressed := gzip.NewWriter(&data)

	archive := tar.NewWriter(compressed)
	for i, header := range headers {
		So(archive.WriteHeader(header), ShouldBeNil)

		if len(contents[i]) > 0 {
			_, err := archive.Write(contents[i])
			So(err, ShouldBeNil)
		}
	}

	So(archive.Close(), ShouldBeNil)
	So(compressed.Close(), ShouldBeNil)

	return data.Bytes()
}

func TestNextflowAcquisitionObjectLimit(t *testing.T) {
	Convey("A 256 MiB plus one byte HTTPS object fails transactionally", t, func() {
		f := newAcquisitionFixture(t)
		f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			f.requests.Add(1)

			if _, err := io.CopyN(w, nextflowLimitReader{}, objectLimit+1); err != nil {
				return
			}
		})
		before := nextflowSnapshot(t, f.cache)
		code, output := f.run(t, commandAcquire)
		So(code, ShouldEqual, 2)
		So(output.Diagnostics[0].Code, ShouldEqual, "E_SOURCE_LIMIT")
		So(output.Counts.VerifiedArtifacts, ShouldEqual, 0)
		So(f.requests.Load(), ShouldEqual, 1)
		So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
	})
}

func nextflowSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()

	result := map[string]string{}

	So(filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, relErr := filepath.Rel(root, name)
		if relErr != nil {
			return relErr
		}

		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}

		result[relative] = info.Mode().String()

		if info.Mode()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(name)
			result[relative] += ":" + target

			return linkErr
		}

		if !entry.IsDir() {
			data, readErr := os.ReadFile(name) //nolint:gosec // Test-owned temporary tree, with no concurrent writers.
			if readErr != nil {
				return readErr
			}

			result[relative] += ":" + testHash(data)
		}

		return nil
	}), ShouldBeNil)

	return result
}

func TestUAT_A1_01(t *testing.T) {
	Convey("Three declared HTTPS blobs publish a valid candidate and validate offline", t, func() {
		f := newAcquisitionFixture(t)
		code, output := f.run(t, commandAcquire)
		So(code, ShouldEqual, 0)
		So(output.Complete, ShouldBeTrue)
		So(output.Counts.VerifiedArtifacts, ShouldEqual, 3)
		So(f.requests.Load(), ShouldEqual, 3)
		f.accept(t)

		for _, item := range f.lock.Artefacts {
			actual, err := os.ReadFile(filepath.Join(f.cache, item.File.Path))
			So(err, ShouldBeNil)
			So(testHash(actual), ShouldEqual, item.File.SHA256)
		}

		f.server.Close()
		f.requests.Store(0)
		code, output = f.run(t, commandValidate)
		So(code, ShouldEqual, 0)
		So(output.Claim, ShouldEqual, "records-valid")
		So(f.requests.Load(), ShouldEqual, 0)
		So(output.Counts.Executed, ShouldEqual, 0)
	})
}

func TestUAT_A1_02(t *testing.T) {
	Convey("All failed and one failed among two successful blobs preserve all previous bytes", t, func() {
		for _, mode := range []int32{1, 2, 3} {
			f := newAcquisitionFixture(t)

			failureMode := mode
			if mode == 3 {
				code, _ := f.run(t, commandAcquire)
				So(code, ShouldEqual, 0)
				f.accept(t)
				f.requests.Store(0)

				failureMode = 2
			}

			f.fail.Store(failureMode)
			So(os.WriteFile(filepath.Join(f.cache, "nextflow-existing-evidence"), []byte("untouched"), 0600), ShouldBeNil)
			before := nextflowSnapshot(t, f.cache)
			old, err := os.ReadFile(filepath.Join(f.root, "sources.lock.json"))
			So(err, ShouldBeNil)

			code, output := f.run(t, commandAcquire)
			So(code, ShouldEqual, 2)
			So(output.Complete, ShouldBeFalse)
			So(output.Counts.VerifiedArtifacts, ShouldEqual, 0)
			So(output.Diagnostics[0].Code, ShouldEqual, "E_FETCH")

			attempts := 2
			if mode != 1 {
				attempts = 4
			}

			So(f.requests.Load(), ShouldEqual, attempts)
			So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
			after, readErr := os.ReadFile(filepath.Join(f.root, "sources.lock.json"))
			So(readErr, ShouldBeNil)
			So(after, ShouldResemble, old)
		}
	})
}

func TestUAT_A1_03(t *testing.T) {
	Convey("Offline validation diagnoses each source and identity mutation", t, func() {
		for _, mutation := range []string{"byte", "missing", "commit", gitTreeKind, "accounting", "mode", "extra"} {
			f := newAcquisitionFixture(t)
			code, _ := f.run(t, commandAcquire)
			So(code, ShouldEqual, 0)
			f.accept(t)
			f.server.Close()
			f.requests.Store(0)

			expected := "E_TREE_INCOMPLETE"

			switch mutation {
			case "byte":
				So(os.WriteFile(sourceFixturePath(f), []byte("changed"), 0600), ShouldBeNil)
				So(os.Chmod(sourceFixturePath(f), 0644), ShouldBeNil)

				expected = sourceHashCode
			case "missing":
				So(os.Remove(sourceFixturePath(f)), ShouldBeNil)

				expected = "E_SOURCE_MISSING"
			case "commit":
				f.lock.Revision = strings.Repeat("a", 40)
				expected = nextflowTargetIdentityCode
			case gitTreeKind:
				f.lock.Tree.Truncated = true
			case "accounting":
				f.lock.Files[0].Blob = strings.Repeat("a", 40)
			case "mode":
				So(os.Chmod(sourceFixturePath(f), 0755), ShouldBeNil)

				expected = sourceHashCode
			case "extra":
				So(os.WriteFile(sourceFixturePath(f)+".extra", []byte("unexpected"), 0600), ShouldBeNil)
			}

			f.save(t)
			code, output := f.run(t, commandValidate)
			So(code, ShouldEqual, 2)
			So(output.Complete, ShouldBeFalse)
			So(output.Diagnostics[0].Code, ShouldEqual, expected)
			So(f.requests.Load(), ShouldEqual, 0)
		}
	})
}

func TestNextflowAcquisitionTreeIdentity(t *testing.T) {
	Convey("A self-consistent replacement tree cannot publish a candidate for the pinned commit", t, func() {
		f := newAcquisitionFixture(t)
		name := "nextflow.txt"
		content := []byte("a different source tree\n")
		blob := testGitHash("blob", content)
		raw, err := hex.DecodeString(blob)
		So(err, ShouldBeNil)

		f.lock.Tree.SHA = testGitHash("tree", append([]byte("100644 "+name+"\x00"), raw...))
		f.lock.Tree.Entries = []treeEntry{{Path: name, SHA: blob, Mode: "100644", Type: "blob"}}
		f.lock.Files = []sourceFile{{Path: name, Blob: blob, Mode: "100644", SHA256: testHash(content),
			Bytes: int64(len(content)), State: sourceSelected}}
		archive := testArchive(t, []*tar.Header{{Name: "repository/" + name, Typeflag: tar.TypeReg,
			Mode: 0644, Size: int64(len(content))}}, [][]byte{content})

		f.blobs["/SOURCE"] = archive
		for i := range f.lock.Artefacts {
			if f.lock.Artefacts[i].Role == roleSource {
				f.lock.Artefacts[i].File.SHA256 = testHash(archive)
				f.lock.Artefacts[i].File.Bytes = int64(len(archive))
			}
		}

		f.save(t)
		before := nextflowSnapshot(t, f.cache)
		code, output := f.run(t, commandAcquire)
		So(code, ShouldEqual, 2)
		So(output.Diagnostics[0].Code, ShouldEqual, nextflowTargetIdentityCode)
		So(output.Complete, ShouldBeFalse)
		So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
	})
}

func testGitHash(kind string, data []byte) string {
	sum := sha1.Sum(append(fmt.Appendf(nil, "%s %d\x00", kind, len(data)), data...)) //nolint:gosec // Git object identity.

	return hex.EncodeToString(sum[:])
}

func TestUAT_A1_04(t *testing.T) {
	Convey("Unsafe extracted members and oversized responses never publish or alter prior bytes", t, func() {
		for _, mutation := range []string{"traversal", "absolute", "duplicate", "symlink", "limit"} {
			f := newAcquisitionFixture(t)
			expected := "E_SOURCE_PATH"

			options := acquisitionOptions{client: f.server.Client()}
			if mutation == "limit" {
				options.maxObject = int64(len(f.blobs["/POM"]) - 1)
				expected = "E_SOURCE_LIMIT"
			} else {
				f.badArchive(t, mutation)
			}

			So(os.WriteFile(filepath.Join(f.cache, "nextflow-existing-evidence"), []byte("untouched"), 0600), ShouldBeNil)
			So(os.WriteFile(filepath.Join(f.cache, "sources.lock.candidate.json"),
				[]byte("previous candidate"), 0600), ShouldBeNil)
			before := nextflowSnapshot(t, f.cache)
			old, err := os.ReadFile(filepath.Join(f.root, "sources.lock.json"))
			So(err, ShouldBeNil)

			code, output := f.runContext(t, context.Background(), commandAcquire, options)
			So(code, ShouldEqual, 2)
			So(output.Diagnostics[0].Code, ShouldEqual, expected)
			So(output.Complete, ShouldBeFalse)
			So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
			after, readErr := os.ReadFile(filepath.Join(f.root, "sources.lock.json"))
			So(readErr, ShouldBeNil)
			So(after, ShouldResemble, old)

			_, statErr := os.Stat(filepath.Join(filepath.Dir(f.cache), "nextflow-escape"))
			So(os.IsNotExist(statErr), ShouldBeTrue)
		}
	})
}

func TestNextflowAcquisitionRequests(t *testing.T) {
	Convey("Cancellation is bounded and preserves a previous candidate", t, func() {
		f := newAcquisitionFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.requests.Add(1)
			cancel()
			<-r.Context().Done()
		})
		before := nextflowSnapshot(t, f.cache)
		started := time.Now()
		code, output := f.runContext(t, ctx, commandAcquire, acquisitionOptions{client: f.server.Client()})
		So(code, ShouldEqual, 2)
		So(output.Complete, ShouldBeFalse)
		So(time.Since(started), ShouldBeLessThan, time.Second*5)
		So(f.requests.Load(), ShouldEqual, 1)
		So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
	})
}

func TestUAT_A1_05(t *testing.T) {
	Convey("Missing runtime distribution or altered Java bytes stop offline preflight", t, func() {
		for _, mutation := range []string{roleRuntime, "java", "extra-java", "java-mode"} {
			f, options, home := nextflowPinnedFixture(t)
			code, _ := f.runContext(t, context.Background(), commandAcquire, options, "--java-home", home)
			So(code, ShouldEqual, 0)
			f.accept(t)
			f.server.Close()
			f.requests.Store(0)

			expected := nextflowLocalHashFailure

			switch mutation {
			case roleRuntime:
				for _, item := range f.lock.Artefacts {
					if item.Role == roleRuntime {
						So(os.Remove(filepath.Join(f.cache, item.File.Path)), ShouldBeNil)
					}
				}

				expected = "E_RUNTIME_MISSING"
			case "extra-java":
				name := filepath.Join(f.cache, f.lock.Environment.JavaHome, "unexpected.jar")
				So(os.WriteFile(name, []byte("injected"), 0600), ShouldBeNil)
			case "java":
				name := filepath.Join(f.cache, f.lock.Environment.JavaHome, "release")
				So(os.WriteFile(name, []byte("changed"), 0600), ShouldBeNil)
			case "java-mode":
				So(os.Chmod(filepath.Join(f.cache, f.lock.Environment.JavaHome, "bin/java"), 0600), ShouldBeNil)
			}

			code, output := f.run(t, commandValidate)
			So(code, ShouldEqual, 2)
			So(output.Diagnostics[0].Code, ShouldEqual, expected)
			So(f.requests.Load(), ShouldEqual, 0)
			So(output.Counts.Executed, ShouldEqual, 0)
		}
	})
}

func TestAcquisitionOutputSafety(t *testing.T) {
	Convey("A symlinked candidate directory cannot redirect writes outside the cache", t, func() {
		f := newAcquisitionFixture(t)
		outside := t.TempDir()
		So(os.Symlink(outside, filepath.Join(f.cache, "redirect")), ShouldBeNil)

		var out, diagnostics bytes.Buffer

		code := runCLI(context.Background(), []string{commandAcquire, rootFlag, f.root, cacheFlag, f.cache,
			"--lock-candidate", filepath.Join(f.cache, "redirect", "lock.json")}, &out, &diagnostics,
			acquisitionOptions{client: f.server.Client()})
		So(code, ShouldEqual, 2)
		So(out.String(), ShouldContainSubstring, "E_SOURCE_PATH")

		entries, err := os.ReadDir(outside)
		So(err, ShouldBeNil)
		So(entries, ShouldBeEmpty)
	})
}

func TestNextflowCandidateInputs(t *testing.T) {
	Convey("Candidate publication preserves input files and complete input trees", t, func() {
		for _, destination := range []string{"java-file", "new-java-file", "source-file", "new-source-file",
			roleSource, roleRuntime, roleMetadata, "distinct", "replacement", "sibling"} {
			Convey(destination, func() {
				f := newAcquisitionFixture(t)
				code, _ := f.run(t, commandAcquire)
				So(code, ShouldEqual, 0)
				f.accept(t)

				candidate, allowed := nextflowCandidatePath(f, destination)
				if destination == "replacement" {
					So(os.WriteFile(candidate, []byte("previous candidate"), 0600), ShouldBeNil)
				}

				before := nextflowSnapshot(t, f.cache)
				corpusBefore := nextflowSnapshot(t, f.root)

				code, output := f.runContext(t, context.Background(), commandAcquire,
					acquisitionOptions{client: f.server.Client()}, "--lock-candidate", candidate)
				if allowed {
					So(code, ShouldEqual, 0)
					So(output.Complete, ShouldBeTrue)

					data, err := os.ReadFile(candidate)
					So(err, ShouldBeNil)
					So(os.WriteFile(filepath.Join(f.root, "sources.lock.json"), data, 0600), ShouldBeNil)
					f.server.Close()
					code, _ = f.run(t, commandValidate)
					So(code, ShouldEqual, 0)
				} else {
					So(code, ShouldEqual, 2)
					So(output.Complete, ShouldBeFalse)
					So(output.Counts.VerifiedArtifacts, ShouldEqual, 0)
					So(output.Diagnostics[0].Code, ShouldEqual, "E_SOURCE_PATH")
					So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
					So(nextflowSnapshot(t, f.root), ShouldResemble, corpusBefore)
				}
			})
		}
	})
}

func nextflowCandidatePath(f *acquisitionFixture, destination string) (string, bool) {
	switch destination {
	case "java-file":
		return filepath.Join(f.cache, f.lock.Environment.JavaHome, "release"), false
	case "new-java-file":
		return filepath.Join(f.cache, f.lock.Environment.JavaHome, "new", "candidate.json"), false
	case "source-file":
		return sourceFixturePath(f), false
	case "new-source-file":
		return filepath.Join(filepath.Dir(sourceFixturePath(f)), "candidate.json"), false
	case roleSource, roleRuntime, roleMetadata:
		for _, item := range f.lock.Artefacts {
			if item.Role == destination {
				return filepath.Join(f.cache, item.File.Path), false
			}
		}
	case "sibling":
		return filepath.Join(f.cache, f.lock.Environment.JavaHome+"-candidate.json"), true
	}

	return filepath.Join(f.cache, destination+".json"), true
}

func sourceFixturePath(f *acquisitionFixture) string {
	for _, item := range f.lock.Artefacts {
		if item.Role == roleSource {
			return filepath.Join(f.cache, item.File.Path+"-tree", nextflowFixtureSource)
		}
	}

	return ""
}

func TestNextflowArchiveRootSafety(t *testing.T) {
	Convey("Root headers retain the full pinned tree and enforce symlink safety", t, func() {
		for _, layout := range []struct {
			name, target string
			allowed      bool
		}{
			{"escaping-link", "../../nextflow-escape", false},
			{"absolute-link", "/nextflow-escape", false},
			{"backslash-link", "unsafe\\/..", false},
			{"empty-link", "", false},
			{"root-link", ".", true},
			{"root-slash-link", "./", true},
			{"normalised-root-link", "safe/..", true},
			{"normalised-child-link", "safe/../child", true},
			{"safe-link", "safe", true},
			{packFile, "", true},
		} {
			Convey(layout.name, func() {
				f := newAcquisitionFixture(t)
				data := nextflowRootArchive(t, f.blobs["/SOURCE"], layout.name, layout.target)

				f.blobs["/SOURCE"] = data
				for i := range f.lock.Artefacts {
					if f.lock.Artefacts[i].Role == roleSource {
						f.lock.Artefacts[i].File.SHA256 = testHash(data)
						f.lock.Artefacts[i].File.Bytes = int64(len(data))
					}
				}

				f.save(t)
				So(os.WriteFile(filepath.Join(f.cache, "sources.lock.candidate.json"),
					[]byte("previous candidate"), 0600), ShouldBeNil)
				before := nextflowSnapshot(t, f.cache)

				code, output := f.run(t, commandAcquire)
				if layout.allowed {
					So(code, ShouldEqual, 0)
					So(output.Complete, ShouldBeTrue)
					So(output.Counts.VerifiedArtifacts, ShouldEqual, 3)
					f.accept(t)
					f.server.Close()
					code, _ = f.run(t, commandValidate)
					So(code, ShouldEqual, 0)
				} else {
					So(code, ShouldEqual, 2)
					So(output.Diagnostics[0].Code, ShouldEqual, "E_SOURCE_PATH")
					So(output.Complete, ShouldBeFalse)
					So(output.Counts.VerifiedArtifacts, ShouldEqual, 0)
					So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
				}
			})
		}
	})
}

func nextflowRootArchive(t *testing.T, original []byte, layout, target string) []byte {
	t.Helper()

	compressed, err := gzip.NewReader(bytes.NewReader(original))
	So(err, ShouldBeNil)

	defer compressed.Close()

	reader := tar.NewReader(compressed)

	var data bytes.Buffer

	output := gzip.NewWriter(&data)
	writer := tar.NewWriter(output)
	changed := 0

	for {
		var header *tar.Header

		header, err = reader.Next()
		if errors.Is(err, io.EOF) {
			err = nil

			break
		}

		if err != nil {
			break
		}

		if header.Typeflag == tar.TypeDir && !strings.Contains(strings.TrimSuffix(header.Name, "/"), "/") {
			header.Typeflag, header.Size = tar.TypeSymlink, 0

			header.Linkname = target
			if layout == packFile {
				header.Typeflag = tar.TypeReg
				header.Name = strings.TrimSuffix(header.Name, "/")
			}

			changed++
		}

		if err = writer.WriteHeader(header); err != nil {
			break
		}

		_, err = io.CopyN(writer, reader, header.Size)
		if err != nil {
			break
		}
	}

	So(err, ShouldBeNil)
	So(changed, ShouldEqual, 1)
	So(writer.Close(), ShouldBeNil)
	So(output.Close(), ShouldBeNil)

	return data.Bytes()
}

func TestUAT_A1_06(t *testing.T) {
	Convey("Pinned acquisition retains the opaque distribution, real Java and dependency metadata", t, func() {
		f, options, home := nextflowPinnedFixture(t)
		nextflowRetainRuntimeFixture(t, f)
		code, output := f.runContext(t, context.Background(), commandAcquire, options, "--java-home", home)
		So(code, ShouldEqual, 0)
		So(output.Complete, ShouldBeTrue)
		f.accept(t)

		roles := map[string]int{}
		mismatches := 0

		for _, item := range f.lock.Artefacts {
			roles[item.Role]++

			data, err := os.ReadFile(filepath.Join(f.cache, item.File.Path))
			if err != nil || testHash(data) != item.File.SHA256 || int64(len(data)) != item.File.Bytes {
				mismatches++
			}

			if item.Role == roleRuntime {
				So(item.Packaging, ShouldEqual, packOpaque)
				So(data, ShouldResemble, f.blobs["/RUNTIME_NEXTFLOW"])
				info, err := os.Stat(filepath.Join(f.cache, item.File.Path))
				So(err, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, 0755)
				So(len(item.Dependencies), ShouldBeGreaterThan, 1)
			}
		}

		So(mismatches, ShouldEqual, 0)
		nextflowAssertRuntimeInventory(t, f)
		nextflowAssertToolClosure(t, f)
		So(roles[roleRuntime], ShouldEqual, 1)
		So(roles[roleJAR], ShouldEqual, 0)
		So(roles[roleMetadata], ShouldEqual, 3)
		So(roles[roleJava], ShouldBeGreaterThan, 0)
		So(roles[roleEnvironment], ShouldBeGreaterThan, 0)
		So(f.lock.Environment.JavaVersion, ShouldContainSubstring, `version "21.`)
		So(len(f.lock.Environment.JavaFiles), ShouldBeGreaterThan, 100)
		f.server.Close()
		f.requests.Store(0)
		code, output = f.run(t, commandValidate)
		So(code, ShouldEqual, 0)
		So(output.Complete, ShouldBeTrue)
		So(f.requests.Load(), ShouldEqual, 0)
	})
}

func nextflowRetainRuntimeFixture(t *testing.T, f *acquisitionFixture) {
	t.Helper()

	root := os.Getenv("NEXTFLOW_RUNTIME_EVIDENCE")
	if root == "" {
		return
	}

	absolute, err := filepath.Abs(root)
	So(err, ShouldBeNil)

	f.cache = filepath.Join(absolute, "nextflow-fixture-cache")
	corpus := filepath.Join(absolute, "nextflow-fixture-corpus")
	So(os.CopyFS(corpus, os.DirFS(f.root)), ShouldBeNil)
	f.root = corpus
	t.Logf("Nextflow retained fixture cache=%s corpus=%s", f.cache, f.root)
}

func nextflowAssertRuntimeInventory(t *testing.T, f *acquisitionFixture) {
	t.Helper()

	expected := map[string]bool{}
	for _, item := range f.lock.Artefacts {
		expected[item.File.Path] = true
	}

	for _, file := range f.lock.Environment.JavaFiles {
		expected[filepath.ToSlash(filepath.Join(f.lock.Environment.JavaHome, file.File.Path))] = true
	}

	for _, item := range f.lock.Artefacts {
		if item.Role != roleSource {
			continue
		}

		for _, file := range f.lock.Files {
			expected[filepath.ToSlash(filepath.Join(item.File.Path+"-tree", file.Path))] = true
		}
	}

	stage := filepath.Dir(f.lock.Environment.JavaHome)
	expected[filepath.ToSlash(filepath.Join(stage, "nextflow-local-provenance.json"))] = true
	unexpected := []string{}
	err := filepath.WalkDir(filepath.Join(f.cache, stage), func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(f.cache, name)
		if err != nil {
			return err
		}

		if !expected[filepath.ToSlash(relative)] {
			unexpected = append(unexpected, relative)
		}

		return nil
	})
	So(err, ShouldBeNil)
	So(unexpected, ShouldBeEmpty)
}

func nextflowAssertToolClosure(t *testing.T, f *acquisitionFixture) {
	t.Helper()

	items := map[string]artefact{}
	for _, item := range f.lock.Artefacts {
		items[item.ID] = item
	}

	for _, name := range []string{"AWK", "BASH", "ENV", "WHICH"} {
		So(len(items["NEXTFLOW_TOOL_"+name].Dependencies), ShouldBeGreaterThan, 0)
	}

	So(items["NEXTFLOW_TOOL_WHICH"].Dependencies, ShouldContain, "NEXTFLOW_TOOL_SH")

	names := make([]string, 0, len(items["NEXTFLOW_TOOL_AWK"].Dependencies))
	for _, id := range items["NEXTFLOW_TOOL_AWK"].Dependencies {
		names = append(names, filepath.Base(items[id].File.Path))
		So(items[id].Role, ShouldEqual, roleEnvironment)
		So(items[id].Origin, ShouldEqual, localOriginPrefix+items[id].File.SHA256)
	}

	for _, name := range []string{"libsigsegv.so.2", "libreadline.so.8", "libmpfr.so.6", "ld-linux-x86-64.so.2"} {
		So(names, ShouldContain, name)
	}

	nextflowAssertLocalProvenance(t, f)
}

func TestNextflowCompanionOffline(t *testing.T) {
	Convey("Companion corruption and unsafe snapshots fail offline before execution", t, func() {
		for _, mutation := range []string{"companion-byte", "companion-missing", "companion-symlink"} {
			Convey(mutation, func() {
				f, options, home := nextflowPinnedFixture(t)
				code, _ := f.runContext(t, context.Background(), commandAcquire, options, "--java-home", home)
				So(code, ShouldEqual, 0)
				f.accept(t)
				f.server.Close()
				f.requests.Store(0)
				index := slices.IndexFunc(f.lock.Artefacts, func(item artefact) bool {
					return strings.HasPrefix(item.ID, "NEXTFLOW_COMPANION_")
				})
				So(index, ShouldBeGreaterThanOrEqualTo, 0)
				name := filepath.Join(f.cache, f.lock.Artefacts[index].File.Path)
				expected := nextflowRuntimeHashCode

				switch mutation {
				case "companion-byte":
					So(os.WriteFile(name, []byte("corrupt"), 0600), ShouldBeNil)
				case "companion-missing":
					So(os.Remove(name), ShouldBeNil)

					expected = nextflowLocalMissingFailure
				case "companion-symlink":
					So(os.Remove(name), ShouldBeNil)
					So(os.Symlink(home, name), ShouldBeNil)

					expected = nextflowCompanionPathCode
				}

				code, output := f.run(t, commandValidate)
				So(code, ShouldEqual, 2)
				So(output.Diagnostics[0].Code, ShouldEqual, expected)
				So(output.Counts.Executed, ShouldEqual, 0)
				So(f.requests.Load(), ShouldEqual, 0)
			})
		}
	})
}

func TestNextflowInterpreterSnapshot(t *testing.T) {
	Convey("Script interpreter acquisition preserves original modes and rejects directories transactionally", t, func() {
		f, options, home := nextflowPinnedFixture(t)
		bin := t.TempDir()
		which := filepath.Join(bin, "which")
		So(os.WriteFile(which, []byte("#!/bin/sh\nexit 0\n"), 0600), ShouldBeNil)
		So(os.Chmod(which, 0711), ShouldBeNil)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		code, _ := f.runContext(t, context.Background(), commandAcquire, options, "--java-home", home)
		So(code, ShouldEqual, 0)

		candidate, err := os.ReadFile(filepath.Join(f.cache, "sources.lock.candidate.json"))
		So(err, ShouldBeNil)

		var lock lockRecord
		So(json.Unmarshal(candidate, &lock), ShouldBeNil)

		for _, item := range lock.Artefacts {
			if item.ID == "NEXTFLOW_TOOL_WHICH" {
				info, statErr := os.Stat(filepath.Join(f.cache, item.File.Path))
				So(statErr, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, 0711)
			}
		}

		before := nextflowSnapshot(t, f.cache)

		So(os.WriteFile(which, []byte("#!"+bin+"\n"), 0600), ShouldBeNil)

		code, output := f.runContext(t, context.Background(), commandAcquire, options, "--java-home", home)
		So(code, ShouldEqual, 2)
		So(output.Diagnostics[0].Code, ShouldEqual, nextflowCompanionPathCode)
		So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
	})
}

func TestUAT_A1_07(t *testing.T) {
	Convey("Opaque runtime identity rejects independent prefix and payload mutations offline", t, func() {
		for _, mutation := range []string{"prefix", "payload", packagingField, "replacement-hash"} {
			Convey(mutation, func() {
				f := newAcquisitionFixture(t)
				code, _ := f.run(t, commandAcquire)
				So(code, ShouldEqual, 0)
				f.accept(t)
				f.server.Close()
				f.requests.Store(0)
				index := slices.IndexFunc(f.lock.Artefacts, func(a artefact) bool { return a.Role == roleRuntime })
				item := &f.lock.Artefacts[index]
				name := filepath.Join(f.cache, item.File.Path)
				data, err := os.ReadFile(name)
				So(err, ShouldBeNil)

				expected := nextflowLocalHashFailure

				switch mutation {
				case "prefix":
					data[100] ^= 1
				case "payload":
					data[20000] ^= 1
				case packagingField:
					item.Packaging = packFile
					expected = nextflowTargetIdentityCode
				case "replacement-hash":
					data[20000] ^= 1
					item.File.SHA256 = testHash(data)
					expected = nextflowTargetIdentityCode
				}

				So(os.WriteFile(name, data, 0600), ShouldBeNil)
				f.save(t)
				code, output := f.run(t, commandValidate)
				So(code, ShouldEqual, 2)
				So(output.Diagnostics[0].Code, ShouldEqual, expected)
				So(output.Counts.Executed, ShouldEqual, 0)
				So(f.requests.Load(), ShouldEqual, 0)
			})
		}
	})
}

func TestNextflowExecutionPermissions(t *testing.T) {
	Convey("Offline preflight and local reuse reject nonexecutable tools and interpreters", t, func() {
		f, options, home := nextflowPinnedFixture(t)
		bin := t.TempDir()
		interpreter, err := os.ReadFile("/bin/dash")
		So(err, ShouldBeNil)
		So(os.WriteFile(filepath.Join(bin, "interpreter"), interpreter, 0600), ShouldBeNil)
		So(os.Chmod(filepath.Join(bin, "interpreter"), 0700), ShouldBeNil)
		which := []byte("#!" + filepath.Join(bin, "interpreter") + "\nexit 0\n")
		So(os.WriteFile(filepath.Join(bin, "which"), which, 0600), ShouldBeNil)
		So(os.Chmod(filepath.Join(bin, "which"), 0700), ShouldBeNil)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		code, _ := f.runContext(t, context.Background(), commandAcquire, options, "--java-home", home)
		So(code, ShouldEqual, 0)
		f.accept(t)
		stage := filepath.Dir(filepath.Join(f.cache, f.lock.Environment.JavaHome))
		So(os.Remove(filepath.Join(stage, "nextflow-local-provenance.json")), ShouldBeNil)

		for _, name := range []string{"bash", "which", "interpreter", "ld-linux-x86-64.so.2", "libc.so.6"} {
			Convey(name, func() {
				index := slices.IndexFunc(f.lock.Artefacts, func(item artefact) bool {
					return item.Role == roleEnvironment && filepath.Base(item.File.Path) == name
				})
				So(index, ShouldBeGreaterThanOrEqualTo, 0)
				item := f.lock.Artefacts[index]
				file := filepath.Join(f.cache, item.File.Path)
				info, err := os.Stat(file)
				So(err, ShouldBeNil)
				So(os.Chmod(file, 0644), ShouldBeNil)

				defer func() { So(os.Chmod(file, info.Mode().Perm()), ShouldBeNil) }()

				before := nextflowSnapshot(t, f.cache)
				f.requests.Store(0)
				validateCode, validation := f.runContext(t, context.Background(), commandValidate, options)
				So(f.requests.Load(), ShouldEqual, 0)
				acquireCode, acquisition := f.runContext(t, context.Background(), commandAcquire, options)

				for i, output := range []result{validation, acquisition} {
					code := []int{validateCode, acquireCode}[i]
					if name == "libc.so.6" {
						So(code, ShouldEqual, 0)
						So(output.Complete, ShouldBeTrue)
					} else {
						So(code, ShouldEqual, 2)
						So(output.Complete, ShouldBeFalse)
						So(output.Diagnostics[0].Code, ShouldEqual, nextflowRuntimeHashCode)
						So(nextflowSnapshot(t, f.cache), ShouldResemble, before)
					}

					So(output.Counts.Executed, ShouldEqual, 0)
				}
			})
		}
	})
}

func TestNextflowAcquisitionUmask(t *testing.T) {
	Convey("Acquisition preserves pinned source and Java permissions under umask 077", t, func() {
		f, options, home := nextflowPinnedFixture(t)
		previous := syscall.Umask(0077)

		defer syscall.Umask(previous)

		code, output := f.runContext(t, context.Background(), commandAcquire, options, "--java-home", home)
		So(code, ShouldEqual, 0)
		So(output.Complete, ShouldBeTrue)
		f.accept(t)
		nextflowAssertLocalProvenance(t, f)
		f.server.Close()
		f.requests.Store(0)
		code, output = f.run(t, commandValidate)
		So(code, ShouldEqual, 0)
		So(output.Complete, ShouldBeTrue)
		So(f.requests.Load(), ShouldEqual, 0)
	})
}

func nextflowAssertLocalProvenance(t *testing.T, f *acquisitionFixture) {
	t.Helper()

	stage := filepath.Dir(filepath.Join(f.cache, f.lock.Environment.JavaHome))
	data, err := os.ReadFile(filepath.Join(stage, "nextflow-local-provenance.json"))
	So(err, ShouldBeNil)

	var observations []nextflowLocalObservation
	So(json.Unmarshal(data, &observations), ShouldBeNil)

	mismatches := 0

	for _, observation := range observations {
		source, sourceErr := os.Stat(observation.Resolved)
		snapshot, snapshotErr := os.Lstat(observation.Snapshot)

		content, contentErr := os.ReadFile(observation.Snapshot)

		invalid := sourceErr != nil || snapshotErr != nil || contentErr != nil || !snapshot.Mode().IsRegular() ||
			source.Mode().Perm() != snapshot.Mode().Perm() || uint32(source.Mode().Perm()) != observation.Mode ||
			testHash(content) != observation.SHA256 || int64(len(content)) != observation.Bytes
		if invalid {
			mismatches++
		}
	}

	So(mismatches, ShouldEqual, 0)
}

// A stream exercises the production byte ceiling without allocating the response
// in the HTTPS fixture. The client must detect the first byte beyond the limit.
type nextflowLimitReader struct{}

func (nextflowLimitReader) Read(buffer []byte) (int, error) {
	clear(buffer)

	return len(buffer), nil
}

// nextflowPinnedTransport keeps pinned acquisition on the independent TLS fixture.
type nextflowPinnedTransport struct {
	upstream    http.RoundTripper
	destination *url.URL
}

func (transport nextflowPinnedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = transport.destination.Scheme
	clone.URL.Host = transport.destination.Host

	return transport.upstream.RoundTrip(clone)
}
