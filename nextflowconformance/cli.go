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
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

const exitInvalid = 2

const suiteRule = "enum:foundation-bootstrap|target-inventory|wr-runtime"

func recordsOf[T any](records map[string]any) []T {
	values := []T{}

	for _, item := range records {
		if value, ok := item.(T); ok {
			values = append(values, value)
		}
	}

	return values
}

type diagnostic struct {
	Code     string  `json:"code"`
	RecordID *string `json:"record_id"`
	Path     *string `json:"path"`
	Message  string  `json:"message"`
}

func errorDiagnostic(err error) diagnostic {
	item := diagnostic{Code: "E_INPUT", Message: err.Error()}

	var detail *sourceError
	if errors.As(err, &detail) {
		item.Code = detail.Code
		if detail.Path != "" {
			item.Path = &detail.Path
		}
	}

	var record *recordError
	if !errors.As(err, &record) {
		return item
	}

	if record.ID != "" {
		item.RecordID = &record.ID
	}

	if record.Path != "" {
		item.Path = &record.Path
	}

	return item
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer, options acquisitionOptions) int {
	invocation, err := parseInvocation(args)

	output := commandResult(invocation)
	if err == nil {
		err = execute(ctx, invocation, &output, options)
	}

	code := 0
	if err != nil {
		code = exitInvalid
		output.Complete = false
		output.Diagnostics = append(output.Diagnostics, errorDiagnostic(err))
		_, _ = fmt.Fprintln(stderr, err)
	}

	if err := json.NewEncoder(stdout).Encode(output); err != nil {
		_, _ = fmt.Fprintln(stderr, err)

		return exitInvalid
	}

	return code
}

func parseInvocation(args []string) (invocation, error) {
	var value invocation
	if len(args) == 0 {
		return value, failure("E_INVOCATION", "", "command required")
	}

	value.command = args[0]

	allowed := commandFlags(value.command)
	if allowed == nil {
		return value, failure("E_INVOCATION", "", "unknown command")
	}

	flags := invocationFlags(&value)
	if err := flags.Parse(args[1:]); err != nil {
		return value, failure("E_INVOCATION", "", err.Error())
	}

	if err := checkInvocation(value, flags, allowed); err != nil {
		return value, err
	}

	return resolveInvocation(value)
}

func commandFlags(command string) []string {
	flags := map[string][]string{
		commandAcquire: {"java-home", "lock-candidate"}, commandValidate: {},
		commandExtract: {"check"}, commandRender: {"check"}, commandDiscover: {suiteField},
		commandRun: {suiteField}, commandVerify: {suiteField}, commandOracle: {caseKind},
	}

	return flags[command]
}

func invocationFlags(value *invocation) *flag.FlagSet {
	flags := flag.NewFlagSet("wr-nextflow-conformance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&value.root, "root", "nextflowconformance/data", "")
	flags.StringVar(&value.cache, "cache", ".tmp/nextflow-conformance", "")
	flags.StringVar(&value.candidate, "lock-candidate", "", "")
	flags.StringVar(&value.javaHome, "java-home", "", "")
	flags.StringVar(&value.suite, suiteField, "", "")
	flags.StringVar(&value.caseID, caseKind, "", "")
	flags.BoolVar(&value.check, "check", false, "")

	return flags
}

func checkInvocation(value invocation, flags *flag.FlagSet, allowed []string) error {
	if err := checkCommandFlags(flags, allowed); err != nil {
		return err
	}

	if slices.Contains(allowed, suiteField) && !validTextRule(value.suite, suiteRule) {
		return failure("E_INVOCATION", suiteField, "known suite required")
	}

	if value.command == commandOracle && !validTextRule(value.caseID, "id") {
		return failure("E_INVOCATION", caseKind, "case ID required")
	}

	return nil
}

func checkCommandFlags(flags *flag.FlagSet, allowed []string) error {
	invalid := ""

	flags.Visit(func(item *flag.Flag) {
		if item.Name != "root" && item.Name != "cache" && !slices.Contains(allowed, item.Name) {
			invalid = item.Name
		}
	})

	if invalid != "" || flags.NArg() != 0 {
		return failure("E_INVOCATION", invalid, "unsupported flag or positional argument")
	}

	return nil
}

func resolveInvocation(value invocation) (invocation, error) {
	var err error

	value.root, err = filepath.Abs(value.root)
	if err != nil {
		return value, err
	}

	value.cache, err = filepath.Abs(value.cache)
	if err != nil {
		return value, err
	}

	if isWithin(value.root, value.cache) || isWithin(value.cache, value.root) {
		return value, failure("E_SOURCE_PATH", value.cache, "cache and corpus must not overlap")
	}

	return resolveCandidate(value)
}

func isWithin(root, name string) bool {
	relative, err := filepath.Rel(root, name)

	return err == nil && (relative == "." || safeRelative(filepath.ToSlash(relative)))
}

func resolveCandidate(value invocation) (invocation, error) {
	var err error

	if value.candidate == "" {
		value.candidate = filepath.Join(value.cache, "sources.lock.candidate.json")
	}

	value.candidate, err = filepath.Abs(value.candidate)
	if err != nil {
		return value, err
	}

	if isWithin(value.root, value.candidate) {
		return value, failure("E_SOURCE_PATH", value.candidate, "candidate would alter source inputs")
	}

	if !isWithin(value.cache, value.candidate) {
		return value, failure("E_SOURCE_PATH", value.candidate, "candidate must remain inside cache")
	}

	for _, name := range []string{value.root, value.cache, value.candidate} {
		if err := rejectSymlinkAncestors(name); err != nil {
			return value, err
		}
	}

	return value, nil
}

func rejectSymlinkAncestors(name string) error {
	for current := name; current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return failure("E_SOURCE_PATH", name, "symlink in output or corpus path")
		}
	}

	return nil
}

func commandResult(args invocation) result {
	output := result{Schema: 1, Command: args.command, Diagnostics: []diagnostic{}}
	claims := map[string]string{
		commandAcquire: "inputs-acquired", commandValidate: "records-valid", commandVerify: args.suite,
	}

	output.Claim = claims[args.command]
	if slices.Contains(commandFlags(args.command), suiteField) && args.suite != "" {
		output.Suite = &args.suite
	}

	return output
}

func execute(ctx context.Context, args invocation, output *result, options acquisitionOptions) error {
	if err := checkCancellation(ctx); err != nil {
		return err
	}

	switch args.command {
	case commandAcquire:
		return executeAcquire(ctx, args, output, options)
	case commandValidate:
		output.Claim = "records-valid"
		if err := validateCorpus(ctx, args.root, args.cache, &output.Counts); err != nil {
			return err
		}

		output.Complete = true

		return nil
	default:
		return failure("E_PREREQUISITE", args.command, "command belongs to a later implementation phase")
	}
}

func checkCancellation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return failure("E_CANCELLED", "", err.Error())
	}

	return nil
}

func executeAcquire(ctx context.Context, args invocation, output *result, options acquisitionOptions) error {
	output.Claim = "inputs-acquired"

	lock, err := acquire(ctx, args.root, args.cache, args.candidate, args.javaHome, options)
	if err != nil {
		return err
	}

	if err = checkCancellation(ctx); err != nil {
		return err
	}

	countLock(lock, &output.Counts)
	output.Counts.VerifiedArtifacts = len(lock.Artefacts)
	output.Complete = true

	return nil
}

func countLock(lock *lockRecord, totals *counts) {
	for _, file := range lock.Files {
		if file.State == sourceSelected {
			totals.SelectedFiles++
		}
	}
}

func validateCorpus(ctx context.Context, root, cache string, totals *counts) error {
	records, err := loadCorpus(ctx, root)
	if err != nil {
		return err
	}

	if err = checkReferences(records); err != nil {
		return err
	}

	lock, err := corpusIdentity(records)
	if err != nil {
		return err
	}

	countRecords(records, totals)

	if err = checkOffline(ctx, cache, lock); err != nil {
		return err
	}

	totals.VerifiedArtifacts = len(lock.Artefacts)

	return nil
}

func loadCorpus(ctx context.Context, root string) (map[string]any, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, failure("E_CORPUS_EMPTY", root, "corpus is missing")
	}

	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, failure("E_CORPUS_EMPTY", root, "corpus contains no records")
	}

	records := map[string]any{}
	if err = loadTarget(ctx, root, records); err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if err = loadCorpusEntry(ctx, root, entry, records); err != nil {
			return nil, err
		}
	}

	return records, nil
}

func loadTarget(ctx context.Context, root string, records map[string]any) error {
	err := loadCorpusFile(ctx, root, "target.json", kindTarget, records)
	if errors.Is(err, os.ErrNotExist) {
		return failure("E_TARGET_MISSING", root, "target.json is required")
	}

	return err
}

func loadCorpusFile(ctx context.Context, root, name, kind string, records map[string]any) error {
	if err := checkCancellation(ctx); err != nil {
		return err
	}

	location := filepath.Join(root, name)
	if err := rejectSymlinkAncestors(location); err != nil {
		return err
	}

	info, err := os.Stat(location)
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return failure("E_INPUT", location, "record must be a regular file")
	}

	data, err := os.ReadFile(location)
	if err != nil {
		return err
	}

	if err = loadRecordFile(kind, data, records); err != nil {
		return &recordError{Path: location, Err: err}
	}

	return checkCancellation(ctx)
}

func loadRecordFile(kind string, data []byte, records map[string]any) error {
	objects, err := recordObjects(kind, data)
	if err != nil {
		return err
	}

	previous := ""

	for _, object := range objects {
		record, decodeErr := decodeRecord(kind, append(bytes.TrimSpace(object), '\n'))
		if decodeErr != nil {
			return decodeErr
		}

		id := reflect.ValueOf(record).Elem().FieldByName("ID").String()
		if _, exists := records[id]; exists || id <= previous {
			return fmt.Errorf("%w: duplicate or unsorted ID %s", errRecord, id)
		}

		previous = id
		records[id] = record
	}

	return nil
}

func recordObjects(kind string, data []byte) ([]json.RawMessage, error) {
	if !validJSONEnvelope(data) {
		return nil, fmt.Errorf("%w: UTF-8 and final newline required", errRecord)
	}

	if slices.Contains([]string{kindTarget, kindLock, kindAttempts}, kind) {
		return []json.RawMessage{data}, nil
	}

	objects := []json.RawMessage{}
	if err := json.Unmarshal(data, &objects); err != nil {
		return nil, err
	}

	if objects == nil {
		return nil, fmt.Errorf("%w: record array cannot be null", errRecord)
	}

	return objects, nil
}

func loadCorpusEntry(ctx context.Context, root string, entry os.DirEntry, records map[string]any) error {
	if entry.Name() == "target.json" {
		return nil
	}

	if entry.Name() == kindAttempts {
		return loadAttempts(ctx, root, records)
	}

	if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
		return nil
	}

	kind := strings.TrimSuffix(entry.Name(), ".json")
	if kind == kindAttempts {
		return failure("E_INPUT", entry.Name(), "attempts require individual attempts/<ID>.json records")
	}

	return loadCorpusFile(ctx, root, entry.Name(), kind, records)
}

func loadAttempts(ctx context.Context, root string, records map[string]any) error {
	directory := filepath.Join(root, kindAttempts)
	if err := rejectSymlinkAncestors(directory); err != nil {
		return err
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err = loadAttempt(ctx, root, entry, records); err != nil {
			return err
		}
	}

	return nil
}

func loadAttempt(ctx context.Context, root string, entry os.DirEntry, records map[string]any) error {
	id := strings.TrimSuffix(entry.Name(), ".json")
	if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !validTextRule(id, "id") {
		return failure("E_INPUT", entry.Name(), "attempt must be an ID-named JSON file")
	}

	attempt := map[string]any{}
	if err := loadCorpusFile(ctx, root, "attempts/"+entry.Name(), kindAttempts, attempt); err != nil {
		return err
	}

	record, exists := attempt[id]
	if !exists {
		return failure("E_INPUT", entry.Name(), "attempt filename and ID differ")
	}

	if _, duplicate := records[id]; duplicate {
		return failure("E_INPUT", entry.Name(), "duplicate attempt ID")
	}

	records[id] = record

	return nil
}

func checkReferences(records map[string]any) error {
	index, err := referenceIndex(records)
	if err != nil {
		return err
	}

	for _, id := range slices.Sorted(maps.Keys(records)) {
		record := records[id]
		if err = checkReferenceFields(reflect.ValueOf(record), index); err != nil {
			return &recordError{ID: id, Err: err}
		}

		if err = checkRecordRelations(record, records, index); err != nil {
			return &recordError{ID: id, Err: err}
		}
	}

	return nil
}

func referenceIndex(records map[string]any) (map[string]referenceTarget, error) {
	index := map[string]referenceTarget{}
	for id, value := range records {
		index[id] = referenceTarget{kind: recordKind(value), value: value}
	}

	for _, value := range records {
		if err := indexNestedReferences(value, index); err != nil {
			return nil, err
		}
	}

	return index, nil
}

func recordKind(value any) string {
	typ := reflect.TypeOf(value)
	for kind, recordType := range recordTypes() {
		if typ == reflect.PointerTo(recordType) {
			return kind
		}
	}

	return ""
}

func indexNestedReferences(value any, index map[string]referenceTarget) error {
	additions := map[string]referenceTarget{}

	switch record := value.(type) {
	case *uatRecord:
		for _, item := range record.Cases {
			additions[item.ID] = referenceTarget{kind: caseKind, value: item}
		}
	case *lockRecord:
		for _, item := range record.Artefacts {
			additions[item.ID] = referenceTarget{kind: artefactKind, value: item}
		}
	default:
	}

	for id, item := range additions {
		if _, exists := index[id]; exists {
			return failure("E_REFERENCE", id, "duplicate nested record ID")
		}

		index[id] = item
	}

	return nil
}

func checkReferenceFields(value reflect.Value, index map[string]referenceTarget) error {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}

		return checkReferenceFields(value.Elem(), index)
	}

	switch value.Kind() {
	case reflect.Struct:
		return checkStructReferences(value, index)
	case reflect.Slice:
		return checkArrayReferences(value, index)
	default:
	}

	return nil
}

func checkStructReferences(value reflect.Value, index map[string]referenceTarget) error {
	for i := range value.NumField() {
		field := value.Type().Field(i)
		if err := checkFieldReference(value.Field(i), field.Tag.Get("ref"), index); err != nil {
			return err
		}
	}

	return nil
}

func checkFieldReference(value reflect.Value, kind string, index map[string]referenceTarget) error {
	if kind == "" {
		return checkReferenceFields(value, index)
	}

	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}

		value = value.Elem()
	}

	if value.Kind() == reflect.Slice {
		return resolveArrayReferences(value, kind, index)
	}

	return resolveReference(value.String(), kind, index)
}

func resolveArrayReferences(value reflect.Value, kind string, index map[string]referenceTarget) error {
	for i := range value.Len() {
		if err := resolveReference(value.Index(i).String(), kind, index); err != nil {
			return err
		}
	}

	return nil
}

func resolveReference(id, kind string, index map[string]referenceTarget) error {
	found, exists := index[id]
	if !exists || (kind != anyRecordKind && kind != found.kind) {
		return failure("E_REFERENCE", "", "missing or wrong-type "+kind+" reference "+id)
	}

	if kind == anyRecordKind && (found.kind == caseKind || found.kind == artefactKind) {
		return failure("E_REFERENCE", "", "expected top-level record "+id)
	}

	return nil
}

func checkArrayReferences(value reflect.Value, index map[string]referenceTarget) error {
	for i := range value.Len() {
		if err := checkReferenceFields(value.Index(i), index); err != nil {
			return err
		}
	}

	return nil
}

func checkRecordRelations(value any, records map[string]any, index map[string]referenceTarget) error {
	if err := checkSourceSpans(reflect.ValueOf(value), lockedSourceReferences(records)); err != nil {
		return err
	}

	if validator, ok := value.(relationValidator); ok {
		return validator.checkRelations(records, index)
	}

	return nil
}

func checkSourceSpans(value reflect.Value, files map[string]fileRef) error {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}

		return checkSourceSpans(value.Elem(), files)
	}

	if item, ok := value.Interface().(span); ok {
		file, exists := files[item.File]
		if !exists || file.SHA256 != item.SHA256 || item.End > file.Bytes {
			return failure("E_REFERENCE", item.File, "source span must match locked file hash and bounds")
		}
	}

	return visitSpanChildren(value, files)
}

func visitSpanChildren(value reflect.Value, files map[string]fileRef) error {
	for _, child := range recordChildren(value) {
		if err := checkSourceSpans(child, files); err != nil {
			return err
		}
	}

	return nil
}

func recordChildren(value reflect.Value) []reflect.Value {
	children := []reflect.Value{}

	switch value.Kind() {
	case reflect.Struct:
		for i := range value.NumField() {
			children = append(children, value.Field(i))
		}
	case reflect.Slice:
		for i := range value.Len() {
			children = append(children, value.Index(i))
		}
	default:
	}

	return children
}

func lockedSourceReferences(records map[string]any) map[string]fileRef {
	sources := map[string]fileRef{}
	for _, file := range sourceFiles(records) {
		sources[file.Path] = fileRef{Path: file.Path, SHA256: file.SHA256, Bytes: file.Bytes}
	}

	for _, lock := range recordsOf[*lockRecord](records) {
		for _, item := range lock.Artefacts {
			if item.Role == roleSource {
				sources[item.File.Path] = item.File
			}
		}
	}

	return sources
}

func sourceFiles(records map[string]any) map[string]sourceFile {
	files := map[string]sourceFile{}

	for _, value := range records {
		if lock, ok := value.(*lockRecord); ok {
			for _, file := range lock.Files {
				files[file.Path] = file
			}
		}
	}

	return files
}

func corpusIdentity(records map[string]any) (*lockRecord, error) {
	targets := recordsOf[*targetRecord](records)

	locks := recordsOf[*lockRecord](records)
	if len(targets) != 1 || len(locks) != 1 {
		return nil, failure("E_TARGET_IDENTITY", "", "exactly one target and lock required")
	}

	if err := checkTargetIdentity(targets[0], locks[0]); err != nil {
		return nil, err
	}

	return locks[0], nil
}

func checkTargetIdentity(target *targetRecord, lock *lockRecord) error {
	identities := []struct{ got, expected string }{
		{target.Version, pinnedVersion}, {target.Tag, pinnedTag}, {target.Commit, pinnedCommit},
		{target.SourceTree, pinnedTree}, {lock.Revision, target.Commit}, {lock.Tree.SHA, target.SourceTree},
	}
	for _, identity := range identities {
		if identity.got != identity.expected {
			return failure("E_TARGET_IDENTITY", target.ID, "target and lock must match pinned release identities")
		}
	}

	return nil
}

func countRecords(records map[string]any, totals *counts) {
	for _, value := range records {
		switch item := value.(type) {
		case *lockRecord:
			countLock(item, totals)
		case *blockRecord:
			countBlock(item, records, totals)
		case *uatRecord:
			totals.UATs++
			totals.DraftUATs += boolCount(item.Readiness == readinessDraft)
		case *requirementRecord:
			totals.Requirements++
		case *decisionRecord:
			totals.UnresolvedDecisions += boolCount(item.State == decisionUnresolved)
		default:
		}
	}
}

func countBlock(item *blockRecord, records map[string]any, totals *counts) {
	files := sourceFiles(records)
	if files[item.Span.File].State != sourceSelected {
		return
	}

	totals.SelectedBlocks++
	totals.PendingBlocks += boolCount(item.Kind == "unclassified")
}

func boolCount(value bool) int {
	if value {
		return 1
	}

	return 0
}

func checkOffline(ctx context.Context, cache string, lock *lockRecord) error {
	if err := checkCancellation(ctx); err != nil {
		return err
	}

	if err := offlinePreflight(cache, lock); err != nil {
		return err
	}

	return checkCancellation(ctx)
}

type recordError struct {
	ID, Path string
	Err      error
}

func (e *recordError) Error() string { return e.ID + ": " + e.Err.Error() }

func (e *recordError) Unwrap() error { return e.Err }

type referenceTarget struct {
	kind  string
	value any
}

func (item *decisionRecord) checkRelations(_ map[string]any, index map[string]referenceTarget) error {
	if item.State == decisionResolved {
		return acceptedReview(item.ReviewID, index)
	}

	return nil
}

func (item *requirementRecord) checkRelations(_ map[string]any, index map[string]referenceTarget) error {
	return checkExclusion(item, index)
}

func checkExclusion(item *requirementRecord, index map[string]referenceTarget) error {
	if item.Scope != scopeExcluded {
		return nil
	}

	for _, id := range item.DecisionIDs {
		decision, ok := index[id].value.(*decisionRecord)
		if !ok || decision.State != decisionResolved || !slices.Contains(decision.AffectedIDs, item.ID) {
			return failure("E_REFERENCE", "", "exclusion needs resolved decision naming "+item.ID)
		}

		if err := acceptedReview(decision.ReviewID, index); err != nil {
			return err
		}
	}

	return nil
}

func (item *obligationRecord) checkRelations(_ map[string]any, index map[string]referenceTarget) error {
	if item.Disposition == dispositionNonrequirement {
		return acceptedReview(item.ReviewID, index)
	}

	return nil
}

func (item *uatRecord) checkRelations(_ map[string]any, index map[string]referenceTarget) error {
	return checkUATRelations(item, index)
}

func checkUATRelations(item *uatRecord, index map[string]referenceTarget) error {
	if item.Readiness != readinessReady {
		return nil
	}

	if err := acceptedReview(item.ReviewID, index); err != nil {
		return err
	}

	if item.Binding == nil {
		return failure("E_REFERENCE", "", "ready UAT needs binding")
	}

	binding, ok := index[*item.Binding].value.(*bindingRecord)
	if !ok || binding.UATID != item.ID || binding.EvidenceKind != item.Kind {
		return failure("E_REFERENCE", "", "UAT binding and evidence kind must agree")
	}

	return nil
}

func (item *bindingRecord) checkRelations(_ map[string]any, index map[string]referenceTarget) error {
	return checkBindingRelation(item, index)
}

func checkBindingRelation(item *bindingRecord, index map[string]referenceTarget) error {
	uat, ok := index[item.UATID].value.(*uatRecord)
	if !ok || uat.Kind != item.EvidenceKind {
		return failure("E_REFERENCE", "", "binding evidence kind differs from UAT")
	}

	return nil
}

func (item *lockRecord) checkRelations(_ map[string]any, index map[string]referenceTarget) error {
	return checkSourceReviews(item, index)
}

func checkSourceReviews(item *lockRecord, index map[string]referenceTarget) error {
	for _, file := range item.Files {
		if file.State == sourceNonsemantic {
			if err := acceptedReview(file.ReviewID, index); err != nil {
				return err
			}
		}
	}

	return nil
}

func acceptedReview(id *string, index map[string]referenceTarget) error {
	if id == nil {
		return failure("E_REFERENCE", "", "accepted independent review required")
	}

	review, ok := index[*id].value.(*reviewRecord)
	if !ok || review.Verdict != reviewAccepted {
		return failure("E_REFERENCE", "", "review must be accepted: "+*id)
	}

	return nil
}

func (item *blockRecord) checkRelations(records map[string]any, index map[string]referenceTarget) error {
	return checkBlockIncludes(item, records, index)
}

func checkBlockIncludes(item *blockRecord, records map[string]any, index map[string]referenceTarget) error {
	files := sourceFiles(records)

	for _, include := range item.IncludeRefs {
		resolved := path.Join(path.Dir(item.Span.File), include.Path)
		if !safeRelative(resolved) {
			return failure("E_REFERENCE", include.Path, "include escapes source tree")
		}

		if err := checkIncludeDestination(include, resolved, files, index); err != nil {
			return err
		}
	}

	return nil
}

func checkIncludeDestination(item includeRef, resolved string, files map[string]sourceFile,
	index map[string]referenceTarget) error {
	if item.BlockID != nil {
		return checkIncludedBlock(*item.BlockID, resolved, files, index)
	}

	if item.ExternalID == nil {
		return failure("E_REFERENCE", resolved, "include destination required")
	}

	external, ok := index[*item.ExternalID].value.(artefact)
	if !ok || external.Role != roleSource || external.File.Path != resolved {
		return failure("E_REFERENCE", resolved, "include must name locked source artefact")
	}

	return nil
}

func checkIncludedBlock(id, resolved string, files map[string]sourceFile, index map[string]referenceTarget) error {
	block, ok := index[id].value.(*blockRecord)
	if !ok || block.Span.File != resolved {
		return failure("E_REFERENCE", resolved, "include block must name resolved source file")
	}

	if _, exists := files[resolved]; !exists {
		return failure("E_REFERENCE", resolved, "include file absent from lock")
	}

	return nil
}

func (item *reviewRecord) checkRelations(records map[string]any, _ map[string]referenceTarget) error {
	sources := lockedSourceReferences(records)
	for _, reference := range item.InputHashes.Source {
		if sources[reference.Path] != reference {
			return failure("E_REFERENCE", reference.Path, "reviewed source must match a locked file path, hash and byte count")
		}
	}

	return nil
}

type relationValidator interface {
	checkRelations(records map[string]any, index map[string]referenceTarget) error
}

type counts struct {
	VerifiedArtifacts   int `json:"verified_artifacts"` //nolint:misspell // Field spelling is fixed by the JSON contract.
	SelectedFiles       int `json:"selected_files"`
	SelectedBlocks      int `json:"selected_blocks"`
	PendingBlocks       int `json:"pending_blocks"`
	DraftUATs           int `json:"draft_uats"`
	Requirements        int `json:"requirements"`
	UATs                int `json:"uats"`
	Discovered          int `json:"discovered"`
	Executed            int `json:"executed"`
	Passed              int `json:"passed"`
	Failed              int `json:"failed"`
	Skipped             int `json:"skipped"`
	TimedOut            int `json:"timed_out"`
	Stale               int `json:"stale"`
	Blocked             int `json:"blocked"`
	UnresolvedDecisions int `json:"unresolved_decisions"`
}

type result struct {
	Schema      int          `json:"schema"`
	Command     string       `json:"command"`
	Suite       *string      `json:"suite"`
	Claim       string       `json:"claim"`
	Complete    bool         `json:"complete"`
	Counts      counts       `json:"counts"`
	Diagnostics []diagnostic `json:"diagnostics"`
}

type invocation struct {
	command, root, cache, candidate, javaHome, suite, caseID string
	check                                                    bool
}

// Run executes the developer CLI and returns its process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runCLI(ctx, args, stdout, stderr, acquisitionOptions{})
}
