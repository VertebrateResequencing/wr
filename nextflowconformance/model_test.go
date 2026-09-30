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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	testSourceID       = "SOURCE"
	testBootstrapSuite = "foundation-bootstrap"
	suiteFlag          = "--suite"
	testRequirementID  = "REQ"
	testBlockID        = "BLOCK"
	testSpanEnd        = `"end": 3`
)

func TestEmptyCorpus(t *testing.T) {
	Convey("An empty corpus cannot claim records-valid", t, func() {
		var out, diagnostics bytes.Buffer

		code := Run(context.Background(), []string{commandValidate, rootFlag, t.TempDir(), cacheFlag,
			t.TempDir()}, &out, &diagnostics)
		So(code, ShouldEqual, 2)
		So(out.String(), ShouldContainSubstring, `"complete":false`)
		So(out.String(), ShouldContainSubstring, `"E_CORPUS_EMPTY"`)
	})
}

func TestMalformedRecords(t *testing.T) {
	Convey("The record boundary rejects malformed JSON", t, func() {
		for _, input := range []string{`{"schema":1,"schema":1}`, `{"schema":1,"unknown":true}`,
			"{\"schema\":1} {}\n", "{\"schema\":1,\"id\":\"\xff\"}\n"} {
			_, err := decodeRecord(kindTarget, []byte(input))
			So(err, ShouldNotBeNil)
		}
	})
}

func TestClosedSchemas(t *testing.T) {
	Convey("Each versioned record round trips and rejects absent or added fields", t, func() {
		kinds := []string{kindTarget, kindLock, kindBlocks, kindObligations, kindRequirements, kindUATs,
			kindReviews, kindDecisions, kindBindings, kindBatches, kindAttempts}
		for _, kind := range kinds {
			data, err := os.ReadFile(filepath.Join("testdata", "records", kind+".json"))
			So(err, ShouldBeNil)
			value, err := decodeRecord(kind, data)
			So(err, ShouldBeNil)
			encoded, err := json.MarshalIndent(value, "", "  ")
			So(err, ShouldBeNil)
			roundTrip, err := decodeRecord(kind, append(encoded, '\n'))
			So(err, ShouldBeNil)
			So(roundTrip, ShouldResemble, value)

			var raw map[string]json.RawMessage
			So(json.Unmarshal(data, &raw), ShouldBeNil)
			raw["unknown"] = json.RawMessage(`true`)
			bad, err := json.Marshal(raw)
			So(err, ShouldBeNil)
			_, err = decodeRecord(kind, append(bad, '\n'))
			So(err, ShouldNotBeNil)
			delete(raw, "unknown")
			delete(raw, "id")
			bad, err = json.Marshal(raw)
			So(err, ShouldBeNil)
			_, err = decodeRecord(kind, append(bad, '\n'))
			So(err, ShouldNotBeNil)
		}
	})
}

func TestRecordConstraints(t *testing.T) {
	Convey("Closed records reject boundary violations without rounding integers", t, func() {
		tests := []struct{ kind, old, new string }{
			{kindBlocks, `"start": 0`, `"start": 3.5`},
			{kindBlocks, testSpanEnd, `"end": -1`},
			{kindBlocks, testSpanEnd, `"end": 9223372036854775808`},
			{kindBlocks, `"input.txt"`, `"../escape"`},
			{kindRequirements, `"Missing inputs fail with an error."`, `"TODO"`},
			{kindRequirements, `"Missing inputs fail with an error."`,
				`"` + "`heading`" + `: TODO: describe expected behaviour."`},
			{kindRequirements, `"origin": "nextflow"`, `"origin": "other"`},
			{kindReviews, `"independent reviewer"`, `"implementor"`},
			{kindDecisions, `"state": "unresolved"`, `"state": "resolved"`},
			{kindUATs, `"readiness": "draft"`, `"readiness": "ready"`},
			{kindAttempts, `"dirty_digest":`, `"unexpected":`},
		}
		for _, test := range tests {
			data, err := os.ReadFile(filepath.Join("testdata", "records", test.kind+".json"))
			So(err, ShouldBeNil)
			So(string(data), ShouldContainSubstring, test.old)
			_, err = decodeRecord(test.kind, bytes.Replace(data, []byte(test.old), []byte(test.new), 1))
			So(err, ShouldNotBeNil)
		}

		data, err := os.ReadFile("testdata/records/blocks.json")
		So(err, ShouldBeNil)
		value, err := decodeRecord(kindBlocks, bytes.Replace(data, []byte(testSpanEnd),
			[]byte(`"end": 9007199254740993`), 1))
		So(err, ShouldBeNil)

		block, ok := value.(*blockRecord)
		So(ok, ShouldBeTrue)
		So(block.Span.End, ShouldEqual, int64(9007199254740993))
	})
}

func TestSchemaEmission(t *testing.T) {
	Convey("Checked schemas match the record definitions", t, func() {
		for _, kind := range []string{kindTarget, kindLock, kindBlocks, kindObligations, kindRequirements,
			kindUATs, kindReviews, kindDecisions, kindBindings, kindBatches, kindAttempts} {
			expected, err := recordSchema(kind)
			So(err, ShouldBeNil)

			name := filepath.Join("data", "schema", kind+".v1.json")
			if os.Getenv("NEXTFLOW_CONFORMANCE_UPDATE_SCHEMAS") == "1" {
				So(os.WriteFile(name, expected, 0600), ShouldBeNil)
			}

			actual, err := os.ReadFile(name)
			So(err, ShouldBeNil)
			So(actual, ShouldResemble, expected)
		}
	})
}

func TestNestedRecordConstraints(t *testing.T) {
	Convey("Nested identities, spans, and discriminants obey the same record contract", t, func() {
		cases := []struct{ kind, old, new string }{
			{kindReviews, testSpanEnd, `"end": -1`},
			{kindReviews, `"start": 0`, `"start": 4`},
			{kindUATs, `"kind": "exit"`, `"kind": "file"`},
			{kindLock, `"pom_id": "POM"`, `"pom_id": "MISSING"`},
		}
		for _, item := range cases {
			data, err := os.ReadFile(filepath.Join("testdata", "records", item.kind+".json"))
			So(err, ShouldBeNil)
			_, err = decodeRecord(item.kind, bytes.Replace(data, []byte(item.old), []byte(item.new), 1))
			So(err, ShouldNotBeNil)
		}
	})
}

func TestAmendedArtefactContract(t *testing.T) {
	Convey("The lock represents opaque runtime bytes and metadata separately", t, func() {
		data, err := os.ReadFile("testdata/records/sources.lock.json")
		So(err, ShouldBeNil)
		_, err = decodeRecord(kindLock, data)
		So(err, ShouldBeNil)

		for _, change := range []struct{ old, replacement string }{
			{`"packaging": "opaque-dist"`, `"packaging": "file"`},
			{`"bytes": 42355106`, `"bytes": 42355105`},
			{`"coordinate": "org.example:example:1.0"`, `"coordinate": null`},
			{`"role": "dependency-metadata"`, `"role": "POM"`},
			{`"dependencies": [
        "JAVA"
      ]`, `"dependencies": ["POM"]`},
		} {
			mutated := bytes.Replace(data, []byte(change.old), []byte(change.replacement), 1)
			So(mutated, ShouldNotResemble, data)
			_, err = decodeRecord(kindLock, mutated)
			So(err, ShouldNotBeNil)
		}
	})
}

func TestCLIInvocationContract(t *testing.T) {
	Convey("Command-specific flags and required selectors fail as bad invocations", t, func() {
		for _, args := range [][]string{
			{commandValidate, suiteFlag, testBootstrapSuite},
			{commandValidate, "--check=false"}, {commandValidate, "--java-home", javaDirectory},
			{commandRun}, {commandDiscover}, {commandVerify}, {commandOracle},
			{commandValidate, "--unknown"}, {commandAcquire, "--case", "CASE"},
		} {
			var output, stderr bytes.Buffer

			code := Run(context.Background(), args, &output, &stderr)
			So(code, ShouldEqual, 2)
			So(output.String(), ShouldContainSubstring, "E_INVOCATION")
		}
	})
	Convey("Cancellation cannot report successful validation", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var output, stderr bytes.Buffer
		So(Run(ctx, []string{commandValidate}, &output, &stderr), ShouldEqual, 2)
		So(output.String(), ShouldContainSubstring, "E_CANCELLED")
	})
}

func TestCorpusContract(t *testing.T) {
	Convey("A nonempty corpus still requires the uniquely named target", t, func() {
		root := t.TempDir()
		data, err := os.ReadFile("testdata/records/requirements.json")
		So(err, ShouldBeNil)
		So(os.WriteFile(filepath.Join(root, "requirements.json"), append(append([]byte("["),
			bytes.TrimSpace(data)...), []byte("]\n")...), 0600), ShouldBeNil)

		var output, stderr bytes.Buffer
		So(Run(context.Background(), []string{commandValidate, rootFlag, root, cacheFlag, t.TempDir()},
			&output, &stderr), ShouldEqual, 2)
		So(output.String(), ShouldContainSubstring, "E_TARGET_MISSING")
	})
	Convey("References resolve to their declared record type", t, func() {
		data, err := os.ReadFile("testdata/records/obligations.json")
		So(err, ShouldBeNil)
		record, err := decodeRecord(kindObligations, data)
		So(err, ShouldBeNil)

		records := map[string]any{"OBLIGATIONS": record, testBlockID: &decisionRecord{ID: testBlockID}}
		So(checkReferences(records), ShouldNotBeNil)
	})
}

func TestRuntimeClosureMembership(t *testing.T) {
	Convey("An external JAR must belong to the opaque runtime closure", t, func() {
		data, err := os.ReadFile("testdata/records/sources.lock.json")
		So(err, ShouldBeNil)
		value, err := decodeRecord(kindLock, data)
		So(err, ShouldBeNil)

		lock, ok := value.(*lockRecord)
		So(ok, ShouldBeTrue)

		extra := lock.Artefacts[0]
		extra.ID, extra.Role, extra.File.Path = "EXTRA_JAR", roleJAR, "extra.jar"
		lock.Artefacts = append([]artefact{extra}, lock.Artefacts...)
		encoded, err := json.Marshal(lock)
		So(err, ShouldBeNil)
		_, err = decodeRecord(kindLock, append(encoded, '\n'))
		So(err, ShouldNotBeNil)
	})
	Convey("Relative includes are resolved against their source file", t, func() {
		data, err := os.ReadFile("testdata/records/blocks.json")
		So(err, ShouldBeNil)

		data = bytes.Replace(data, []byte(`"include_refs": []`),
			[]byte(`"include_refs": [{"path":"../shared.nf","block_id":"SHARED","external_id":null}]`), 1)
		_, err = decodeRecord(kindBlocks, data)
		So(err, ShouldBeNil)
	})
}

func TestFailedCommandResultContract(t *testing.T) {
	Convey("A failed verify retains its requested claim and validation has no suite", t, func() {
		var output, stderr bytes.Buffer

		code := Run(context.Background(), []string{commandVerify, suiteFlag, testBootstrapSuite}, &output, &stderr)
		So(code, ShouldEqual, 2)

		var decoded result
		So(json.Unmarshal(output.Bytes(), &decoded), ShouldBeNil)
		So(decoded.Claim, ShouldEqual, testBootstrapSuite)
		So(decoded.Complete, ShouldBeFalse)
		output.Reset()
		code = Run(context.Background(), []string{commandValidate, suiteFlag, testBootstrapSuite}, &output, &stderr)
		So(code, ShouldEqual, 2)
		So(json.Unmarshal(output.Bytes(), &decoded), ShouldBeNil)
		So(decoded.Suite, ShouldBeNil)
	})
}

func TestLocalOriginDigestEquality(t *testing.T) {
	const artefactsField = "artifacts" //nolint:misspell // Required JSON spelling.

	Convey("Local origins identify the recorded regular-file digest", t, func() {
		data, err := os.ReadFile("testdata/schema-records/sources.lock.local-origins.json")
		So(err, ShouldBeNil)
		_, err = decodeRecord(kindLock, data)
		So(err, ShouldBeNil)

		for _, index := range []float64{0, 3} {
			for _, path := range [][]any{
				{artefactsField, index, "origin"},
				{artefactsField, index, "file", sha256Field},
			} {
				value := strings.Repeat("b", 64)
				if path[len(path)-1] == "origin" {
					value = "local:sha256:" + value
				}

				replacement, marshalErr := json.Marshal(value)
				So(marshalErr, ShouldBeNil)

				_, decodeErr := decodeRecord(kindLock, mutateJSON(t, data, path, replacement))
				So(decodeErr, ShouldNotBeNil)
				So(decodeErr.Error(), ShouldContainSubstring, "local origin must match file.sha256")
			}
		}
	})
}

func TestIndependentSchemaCases(t *testing.T) {
	Convey("Independent record mutations have the declared decoder result", t, func() {
		data, err := os.ReadFile("testdata/schema-cases.json")
		So(err, ShouldBeNil)

		var cases []struct {
			Name, Kind, Fixture string
			Path                []any
			Value               json.RawMessage
			Valid               bool
		}
		So(json.Unmarshal(data, &cases), ShouldBeNil)

		for _, item := range cases {
			Convey(item.Name, func() {
				name := filepath.Join("testdata", "records", item.Kind+".json")
				if item.Fixture != "" {
					name = filepath.Join("testdata", "schema-records", item.Fixture)
				}

				data, readErr := os.ReadFile(name)
				So(readErr, ShouldBeNil)

				mutated := mutateJSON(t, data, item.Path, item.Value)
				_, decodeErr := decodeRecord(item.Kind, mutated)
				So(decodeErr == nil, ShouldEqual, item.Valid)
			})
		}
	})
}

func mutateJSON(t *testing.T, data []byte, path []any, replacement json.RawMessage) []byte {
	t.Helper()

	var object any

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	So(decoder.Decode(&object), ShouldBeNil)

	cursor := object
	for _, component := range path[:len(path)-1] {
		cursor = jsonChild(cursor, component)
	}

	var value any

	decoder = json.NewDecoder(bytes.NewReader(replacement))
	decoder.UseNumber()
	So(decoder.Decode(&value), ShouldBeNil)

	switch container := cursor.(type) {
	case map[string]any:
		key, ok := path[len(path)-1].(string)
		So(ok, ShouldBeTrue)

		container[key] = value
	case []any:
		index, ok := path[len(path)-1].(float64)
		So(ok, ShouldBeTrue)

		container[int(index)] = value
	default:
		So(cursor, ShouldHaveSameTypeAs, map[string]any{})
	}

	encoded, err := json.Marshal(object)
	So(err, ShouldBeNil)

	return append(encoded, '\n')
}

func jsonChild(value, component any) any {
	switch container := value.(type) {
	case map[string]any:
		key, ok := component.(string)
		So(ok, ShouldBeTrue)

		return container[key]
	case []any:
		index, ok := component.(float64)
		So(ok, ShouldBeTrue)

		return container[int(index)]
	default:
		return nil
	}
}

func TestTypedCorpusRelations(t *testing.T) {
	Convey("The independent record fixtures form a well-typed corpus", t, func() {
		records := corpusFixtures(t)
		So(checkReferences(records), ShouldBeNil)
		_, err := corpusIdentity(records)
		So(err, ShouldBeNil)
	})
	Convey("Wrong-type, missing, and unresolved cross-record claims are rejected", t, func() {
		cases := []func(map[string]any){
			func(records map[string]any) {
				recordsOf[*targetRecord](records)[0].Profiles.Bootstrap.RequiredReviews = []string{testRequirementID}
			},
			func(records map[string]any) {
				recordsOf[*targetRecord](records)[0].Profiles.Bootstrap.RequiredCases = []string{testRequirementID}
			},
			func(records map[string]any) {
				recordsOf[*targetRecord](records)[0].Decisions = []string{testRequirementID}
			},
			func(records map[string]any) {
				recordsOf[*blockRecord](records)[0].Children = []string{testRequirementID}
			},
			func(records map[string]any) { recordsOf[*uatRecord](records)[0].FacetIDs = []string{testRequirementID} },
			func(records map[string]any) { recordsOf[*uatRecord](records)[0].RequirementIDs = []string{"MISSING"} },
			func(records map[string]any) { recordsOf[*bindingRecord](records)[0].EvidenceKind = commandOracle },
			func(records map[string]any) {
				recordsOf[*batchRecord](records)[0].DependsOn = []string{testRequirementID}
			},
			func(records map[string]any) { recordsOf[*blockRecord](records)[0].Span.End = 4 },
			func(records map[string]any) {
				recordsOf[*blockRecord](records)[0].Span.SHA256 = strings.Repeat("b", 64)
			},
			func(records map[string]any) { recordsOf[*uatRecord](records)[0].Cases[0].ID = "BINDING" },
			func(records map[string]any) {
				req := recordsOf[*requirementRecord](records)[0]
				req.Scope, req.DecisionIDs = scopeExcluded, []string{"DECISION"}
			},
			func(records map[string]any) {
				decision := recordsOf[*decisionRecord](records)[0]
				decision.State, decision.Resolution, decision.ReviewID = decisionResolved, new("Use local execution."),
					new("REVIEW")
				recordsOf[*reviewRecord](records)[0].Verdict = "changes-required"
			},
		}
		for _, mutate := range cases {
			records := corpusFixtures(t)
			mutate(records)
			So(checkReferences(records), ShouldNotBeNil)
		}
	})
}

func TestAttemptLoadingAndCounts(t *testing.T) {
	Convey("Nested attempts are loaded and record counts survive an offline prerequisite failure", t, func() {
		root, cache := t.TempDir(), t.TempDir()
		writeCorpus(t, root, corpusFixtures(t))
		loaded, err := loadCorpus(context.Background(), root)
		So(err, ShouldBeNil)
		So(recordsOf[*attemptRecord](loaded), ShouldHaveLength, 1)

		var output, stderr bytes.Buffer
		So(Run(context.Background(), []string{commandValidate, rootFlag, root, cacheFlag, cache}, &output,
			&stderr), ShouldEqual, 2)

		var outputResult result
		So(json.Unmarshal(output.Bytes(), &outputResult), ShouldBeNil)
		So(outputResult.Complete, ShouldBeFalse)
		So(outputResult.Counts.Requirements, ShouldEqual, 1)
		So(outputResult.Counts.UATs, ShouldEqual, 1)
		So(outputResult.Counts.DraftUATs, ShouldEqual, 1)
		So(outputResult.Counts.SelectedFiles, ShouldEqual, 1)
		So(outputResult.Counts.SelectedBlocks, ShouldEqual, 1)
		So(outputResult.Counts.UnresolvedDecisions, ShouldEqual, 1)
		So(outputResult.Counts.VerifiedArtifacts, ShouldEqual, 0)
		So(outputResult.Counts.Passed, ShouldEqual, 0)
		So(stderr.Len(), ShouldBeGreaterThan, 0)
	})
	Convey("The attempt filename must match its record identity", t, func() {
		root := t.TempDir()
		writeCorpus(t, root, corpusFixtures(t))
		So(os.Rename(filepath.Join(root, "attempts/ATTEMPTS.json"), filepath.Join(root,
			"attempts/OTHER.json")), ShouldBeNil)
		_, err := loadCorpus(context.Background(), root)
		So(err, ShouldNotBeNil)
	})
	Convey("A malformed nested attempt is never silently ignored", t, func() {
		root := t.TempDir()
		writeCorpus(t, root, corpusFixtures(t))
		So(os.WriteFile(filepath.Join(root, "attempts/ATTEMPTS.json"), []byte("{}\n"), 0600), ShouldBeNil)

		var output, stderr bytes.Buffer
		So(Run(context.Background(), []string{commandValidate, rootFlag, root, cacheFlag, t.TempDir()},
			&output, &stderr), ShouldEqual, 2)
		So(output.String(), ShouldContainSubstring, "attempts/ATTEMPTS.json")
	})
}

func writeCorpus(t *testing.T, root string, records map[string]any) {
	t.Helper()

	for _, record := range records {
		kind := recordKind(record)
		value := record

		name := kind + ".json"
		switch kind {
		case kindTarget, kindLock:
		case kindAttempts:
			attempts := recordsOf[*attemptRecord](records)

			So(os.MkdirAll(filepath.Join(root, kindAttempts), 0700), ShouldBeNil)

			name = "attempts/" + attempts[0].ID + ".json"
		default:
			value = []any{record}
		}

		data, err := json.Marshal(value)
		So(err, ShouldBeNil)
		So(os.WriteFile(filepath.Join(root, name), append(data, '\n'), 0600), ShouldBeNil)
	}
}

func TestRelationalRecordConstraints(t *testing.T) {
	Convey("Lock identities and dependencies cannot contradict their records", t, func() {
		changes := []func(*lockRecord){
			func(lock *lockRecord) { lock.Files = append(lock.Files, lock.Files[0]) },
			func(lock *lockRecord) { lock.Tree.Entries = append(lock.Tree.Entries, lock.Tree.Entries[0]) },
			func(lock *lockRecord) {
				lock.Environment.JavaFiles = append(lock.Environment.JavaFiles, lock.Environment.JavaFiles[0])
			},
			func(lock *lockRecord) {
				lock.Environment.Variables = append(lock.Environment.Variables, lock.Environment.Variables[0])
			},
			func(lock *lockRecord) { lock.Artefacts[0].Dependencies = []string{"RUNTIME"} },
			func(lock *lockRecord) { lock.Artefacts[2].Dependencies = []string{"MISSING"} },
			func(lock *lockRecord) { lock.Environment.Dependencies[0].POMID = "JAVA" },
			func(lock *lockRecord) { lock.Environment.Dependencies[0].ArtifactID = new("POM") },
			func(lock *lockRecord) { lock.Environment.Dependencies[0].Version = "2" },
		}
		for _, change := range changes {
			data, err := os.ReadFile("testdata/records/sources.lock.json")
			So(err, ShouldBeNil)
			value, err := decodeRecord(kindLock, data)
			So(err, ShouldBeNil)

			lock, ok := value.(*lockRecord)
			So(ok, ShouldBeTrue)
			change(lock)
			encoded, err := json.Marshal(lock)
			So(err, ShouldBeNil)
			_, err = decodeRecord(kindLock, append(encoded, '\n'))
			So(err, ShouldNotBeNil)
		}
	})
	Convey("A target mismatch or second target cannot validate the corpus", t, func() {
		records := corpusFixtures(t)
		recordsOf[*targetRecord](records)[0].Commit = strings.Repeat("b", 40)
		_, err := corpusIdentity(records)
		So(err, ShouldNotBeNil)

		records = corpusFixtures(t)
		target := *recordsOf[*targetRecord](records)[0]
		target.ID = "OTHER_TARGET"
		records[target.ID] = &target
		_, err = corpusIdentity(records)
		So(err, ShouldNotBeNil)
	})
	Convey("Quoted literals and ordered integers remain distinct from description and span constraints", t, func() {
		data, err := os.ReadFile("testdata/records/attempts.json")
		So(err, ShouldBeNil)

		data = bytes.Replace(data, []byte(`"ended": "2026-09-28T00:00:01Z"`), []byte(`"ended": "2026-09-27T23:59:59Z"`), 1)
		_, err = decodeRecord(kindAttempts, data)
		So(err, ShouldNotBeNil)
	})
}

func TestReviewSourceFileReferences(t *testing.T) {
	Convey("Reviewed source file references bind actual locked paths and bytes", t, func() {
		records := corpusFixtures(t)
		review := recordsOf[*reviewRecord](records)[0]
		review.InputHashes.Source[0].Path = "missing.txt"

		So(checkReferences(records), ShouldNotBeNil)

		review.InputHashes.Source[0].Path = "input.txt"
		review.InputHashes.Source[0].Bytes++

		So(checkReferences(records), ShouldNotBeNil)
	})
	Convey("A reviewed Maven source artefact binds its acquired bytes", t, func() {
		records := corpusFixtures(t)
		lock := recordsOf[*lockRecord](records)[0]
		source := artefact{
			ID: testSourceID, Role: roleSource, Origin: "https://example.org/semantic-sources.jar",
			File:      fileRef{Path: "semantic-sources.jar", SHA256: strings.Repeat("a", 64), Bytes: 3},
			Packaging: packFile, Coordinate: new("org.example:semantic:1.0"), Dependencies: []string{},
		}
		lock.Artefacts = append(lock.Artefacts, source)
		review := recordsOf[*reviewRecord](records)[0]
		review.InputHashes.Source = []fileRef{source.File}
		review.SourceSpans = []span{{File: source.File.Path, SHA256: source.File.SHA256, Start: 0, End: 3}}

		So(checkReferences(records), ShouldBeNil)

		Convey("An empty span at the final byte boundary is valid", func() {
			review.SourceSpans[0].Start = 3

			So(checkReferences(records), ShouldBeNil)
		})
		Convey("The span cannot extend beyond the acquired bytes", func() {
			review.SourceSpans[0].End = 4

			So(checkReferences(records), ShouldNotBeNil)
		})
		Convey("The span must bind the acquired hash", func() {
			review.SourceSpans[0].SHA256 = strings.Repeat("b", 64)

			So(checkReferences(records), ShouldNotBeNil)
		})
		Convey("The span must name the acquired source path", func() {
			review.SourceSpans[0].File = "missing.groovy"

			So(checkReferences(records), ShouldNotBeNil)
		})
		Convey("An acquired execution artefact cannot supply source spans", func() {
			lock.Artefacts[len(lock.Artefacts)-1].Role = roleJAR

			So(checkReferences(records), ShouldNotBeNil)
		})
	})
}

func corpusFixtures(t *testing.T) map[string]any {
	t.Helper()

	records := map[string]any{}
	replacements := map[string]string{
		"BLOCKS": testBlockID, "OBLIGATIONS": "FACET", "REQUIREMENTS": testRequirementID,
		"UATS": "UAT", "REVIEWS": "REVIEW", "DECISIONS": "DECISION", "BINDINGS": "BINDING",
	}

	for _, kind := range []string{kindTarget, kindLock, kindBlocks, kindObligations, kindRequirements,
		kindUATs, kindReviews, kindDecisions, kindBindings, kindBatches, kindAttempts} {
		data, err := os.ReadFile(filepath.Join("testdata", "records", kind+".json"))
		So(err, ShouldBeNil)

		for old, replacement := range replacements {
			data = bytes.ReplaceAll(data, []byte(`"`+old+`"`), []byte(`"`+replacement+`"`))
		}

		value, err := decodeRecord(kind, data)
		So(err, ShouldBeNil)

		id := reflect.ValueOf(value).Elem().FieldByName("ID").String()
		records[id] = value
	}

	targets := recordsOf[*targetRecord](records)
	targets[0].Tag, targets[0].Commit, targets[0].SourceTree = pinnedTag, pinnedCommit, pinnedTree
	locks := recordsOf[*lockRecord](records)
	locks[0].Revision, locks[0].Tree.SHA = pinnedCommit, pinnedTree

	return records
}
