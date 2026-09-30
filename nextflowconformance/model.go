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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	kindTarget                = "target"
	kindLock                  = "sources.lock"
	kindBlocks                = "blocks"
	kindObligations           = "obligations"
	kindRequirements          = "requirements"
	kindUATs                  = "uats"
	kindReviews               = "reviews"
	kindDecisions             = "decisions"
	kindBindings              = "bindings"
	kindBatches               = "batches"
	kindAttempts              = "attempts"
	roleSource                = "source"
	roleLauncher              = "launcher"
	roleRuntime               = "runtime"
	roleJAR                   = "JAR"
	roleJava                  = "Java"
	roleEnvironment           = "environment-tool"
	roleMetadata              = "dependency-metadata"
	packOpaque                = "opaque-dist"
	packExtracted             = "extracted-archive"
	packFile                  = "file"
	decisionResolved          = "resolved"
	decisionUnresolved        = "unresolved"
	sourceSelected            = "selected"
	sourceNonsemantic         = "nonsemantic"
	dispositionNonrequirement = "nonrequirement"
	readinessDraft            = "draft"
	readinessReady            = "ready"
	scopeExcluded             = "excluded"
	reviewAccepted            = "accepted"
	commandAcquire            = "acquire"
	commandValidate           = "validate"
	commandDiscover           = "discover"
	commandRun                = "run"
	commandVerify             = "verify"
	commandExtract            = "extract"
	commandRender             = "render"
	commandOracle             = "oracle"
	caseKind                  = "case"
	artefactKind              = "artefact"
	anyRecordKind             = "record"
	reviewField               = "review_id"
	httpsRule                 = "https"
	artefactOriginRule        = "artefact-origin"
	localOriginPrefix         = "local:sha256:"
	localOriginPattern        = `^local:sha256:[a-f0-9]{64}$`
	roleField                 = "role"
	packagingField            = "packaging"
	sha256Field               = "sha256"
	javaDirectory             = "java"
	cacheFlag                 = "--cache"
)

const (
	suiteField = "suite"
	textRule   = "text"
	rootFlag   = "--root"
)

var errRecord = errors.New("invalid record")

const distributionBytes = int64(42355106)

// Unicode White_Space matches strings.TrimSpace. Spell out the ranges because
// Go, Python and ECMAScript disagree on which characters \s includes.
const descriptionSpaceCharacters = "\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000"

func meaningfulText(value string) bool {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" || slices.Contains([]string{"todo", "tbd", "placeholder"}, text) {
		return false
	}

	// NUL cannot complete a stand-in word or match its whitespace boundaries.
	// Retain a closing backtick for the archived heading form.
	unquoted := regexp.MustCompile(descriptionQuotedPattern()).ReplaceAllStringFunc(value, func(literal string) string {
		if strings.HasPrefix(literal, "`") {
			return "`"
		}

		if !strings.ContainsAny(literal[:1], "\\\"'") {
			return literal[:strings.IndexByte(literal, '\'')] + "\x00"
		}

		return "\x00"
	})

	return !regexp.MustCompile(descriptionStandInPattern()).MatchString(unquoted)
}

func validIncludePath(value string) bool {
	if value == "" || value == "." || strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") {
		return false
	}

	return path.Clean(value) == value
}

// These private structs are the authoritative record vocabulary. JSON schema
// emission reads the same fields and constraints as decoding.
type fileRef struct {
	Path   string `json:"path"   rule:"path"`
	SHA256 string `json:"sha256" rule:"hash"`
	Bytes  int64  `json:"bytes"  rule:"nonnegative"`
}

type span struct {
	File   string `json:"file"   rule:"path"`
	Start  int64  `json:"start"  rule:"nonnegative"`
	End    int64  `json:"end"    rule:"nonnegative"`
	SHA256 string `json:"sha256" rule:"hash"`
}

func (item span) validate() error {
	if item.End < item.Start {
		return fmt.Errorf("%w: reversed span", errRecord)
	}

	return nil
}

type profile struct {
	RequiredIDs     []string `json:"required_ids"     ref:"record"          rule:"ids,nonempty"`
	RequiredCases   []string `json:"required_cases"   ref:"case"            rule:"ids,nonempty"`
	RequiredReviews []string `json:"required_reviews" ref:"reviews"         rule:"ids,nonempty"`
	RequiredGates   []string `json:"required_gates"   rule:"texts,nonempty"`
}

type profiles struct {
	Bootstrap profile `json:"foundation-bootstrap"`
	Inventory profile `json:"target-inventory"`
	Runtime   profile `json:"wr-runtime"`
}

type targetRecord struct {
	Schema     int      `json:"schema"      rule:"one"`
	ID         string   `json:"id"          rule:"id"`
	Version    string   `json:"version"     rule:"text"`
	Parser     string   `json:"parser"      rule:"enum:v2"`
	Tag        string   `json:"tag"         rule:"git"`
	Commit     string   `json:"commit"      rule:"git"`
	SourceTree string   `json:"source_tree" rule:"git"`
	Profiles   profiles `json:"profiles"`
	Decisions  []string `json:"decisions"   ref:"decisions" rule:"ids"`
}

func recordTypes() map[string]reflect.Type {
	return map[string]reflect.Type{
		kindTarget: reflect.TypeFor[targetRecord](), kindLock: reflect.TypeFor[lockRecord](),
		kindBlocks: reflect.TypeFor[blockRecord](), kindObligations: reflect.TypeFor[obligationRecord](),
		kindRequirements: reflect.TypeFor[requirementRecord](), kindUATs: reflect.TypeFor[uatRecord](),
		kindReviews: reflect.TypeFor[reviewRecord](), kindDecisions: reflect.TypeFor[decisionRecord](),
		kindBindings: reflect.TypeFor[bindingRecord](), kindBatches: reflect.TypeFor[batchRecord](),
		kindAttempts: reflect.TypeFor[attemptRecord](),
	}
}

type treeEntry struct {
	Path string `json:"path" rule:"path"`
	SHA  string `json:"sha"  rule:"git"`
	Mode string `json:"mode" rule:"enum:100644|100755|120000|040000"`
	Type string `json:"type" rule:"enum:blob|tree"`
}

type treeRecord struct {
	SHA       string      `json:"sha"       rule:"git"`
	Origin    string      `json:"origin"    rule:"https"`
	Truncated bool        `json:"truncated"`
	Entries   []treeEntry `json:"entries"   rule:"nonempty"`
}

type sourceFile struct {
	Path      string  `json:"path"      rule:"path"`
	Blob      string  `json:"blob"      rule:"git"`
	Mode      string  `json:"mode"      rule:"enum:100644|100755|120000"`
	SHA256    string  `json:"sha256"    rule:"hash"`
	Bytes     int64   `json:"bytes"     rule:"nonnegative"`
	State     string  `json:"state"     rule:"enum:selected|unreviewed|nonsemantic"`
	Rationale *string `json:"rationale" rule:"text"`
	ReviewID  *string `json:"review_id" ref:"reviews"                               rule:"id"`
}

func (item sourceFile) validate() error {
	if item.State == sourceNonsemantic && (item.Rationale == nil || item.ReviewID == nil) {
		return fmt.Errorf("%w: nonsemantic file needs rationale and review", errRecord)
	}

	return nil
}

type artefact struct {
	ID           string   `json:"id"           rule:"id"`
	Role         string   `json:"role"         rule:"role"`
	Origin       string   `json:"origin"       rule:"artefact-origin"`
	File         fileRef  `json:"file"`
	Packaging    string   `json:"packaging"    rule:"enum:file|extracted-archive|opaque-dist"`
	Coordinate   *string  `json:"coordinate"   rule:"maven"`
	Dependencies []string `json:"dependencies" rule:"ids"`
}

func checkArtefact(item artefact) error {
	if err := checkArtefactOrigin(item); err != nil {
		return err
	}

	opaque := item.Packaging == packOpaque
	if opaque != (item.Role == roleRuntime) {
		return failure("E_TARGET_IDENTITY", item.ID, "runtime must use opaque-dist packaging exclusively")
	}

	if opaque && (item.File.SHA256 != distributionHash || item.File.Bytes != distributionBytes) {
		return failure("E_TARGET_IDENTITY", item.ID, "opaque runtime differs from pinned distribution")
	}

	return checkArtefactCoordinate(item)
}

func checkArtefactOrigin(item artefact) error {
	if !strings.HasPrefix(item.Origin, localOriginPrefix) {
		return nil
	}

	if (item.Role != roleJava && item.Role != roleEnvironment) || item.Packaging != packFile || item.Coordinate != nil {
		return fmt.Errorf("%w: local origin requires Java or environment-tool, file packaging and null coordinate", errRecord)
	}

	if item.Origin != localOriginPrefix+item.File.SHA256 {
		return fmt.Errorf("%w: local origin must match file.sha256", errRecord)
	}

	return nil
}

func checkArtefactCoordinate(item artefact) error {
	metadata := item.Role == roleMetadata

	mavenSource := item.Role == roleSource && strings.HasSuffix(item.File.Path, "-sources.jar")
	if (metadata || mavenSource) != (item.Coordinate != nil) {
		return fmt.Errorf("%w: coordinate is required only for Maven metadata and sources", errRecord)
	}

	if metadata {
		return checkMetadataFile(item)
	}

	if strings.HasSuffix(item.File.Path, ".pom") && !metadata {
		return fmt.Errorf("%w: POM must have dependency-metadata role", errRecord)
	}

	return nil
}

func checkMetadataFile(item artefact) error {
	if item.Packaging != packFile || !strings.HasSuffix(item.File.Path, ".pom") || len(item.Dependencies) != 0 {
		return fmt.Errorf("%w: POM metadata must be a file with no execution dependencies", errRecord)
	}

	return nil
}

func checkRuntimeClosure(ids map[string]artefact) error {
	runtimeID := ""
	done := map[string]bool{}

	for _, item := range ids {
		if item.Role == roleRuntime {
			if runtimeID != "" {
				return failure("E_TARGET_IDENTITY", roleRuntime, "exactly one opaque runtime is required")
			}

			runtimeID = item.ID
		}

		if err := walkDependencies(item.ID, ids, map[string]bool{}, done); err != nil {
			return err
		}
	}

	if runtimeID == "" {
		return failure("E_TARGET_IDENTITY", roleRuntime, "exactly one opaque runtime is required")
	}

	return checkExecutionClosure(runtimeID, ids)
}

func checkCoordinate(item coordinate, ids map[string]artefact) error {
	pom, exists := ids[item.POMID]

	expected := item.Group + ":" + item.Name + ":" + item.Version
	if !exists || pom.Role != roleMetadata || pom.Coordinate == nil || *pom.Coordinate != expected {
		return fmt.Errorf("%w: coordinate %s needs matching POM metadata", errRecord, item.ID)
	}

	return checkCoordinateJar(item, ids)
}

func checkCoordinateJar(item coordinate, ids map[string]artefact) error {
	if item.ArtifactID == nil {
		return nil
	}

	actual, present := ids[*item.ArtifactID]
	if !present || actual.Role != roleJAR {
		return fmt.Errorf("%w: coordinate %s must reference an actual external JAR", errRecord, item.ID)
	}

	return nil
}

func checkExecutionClosure(runtimeID string, ids map[string]artefact) error {
	closure := map[string]bool{}
	if err := walkDependencies(runtimeID, ids, map[string]bool{}, closure); err != nil {
		return err
	}

	for id, item := range ids {
		if closure[id] != executionRole(item.Role) {
			return fmt.Errorf("%w: runtime closure must contain exactly the execution inputs: %s", errRecord, id)
		}
	}

	return nil
}

func executionRole(role string) bool {
	return slices.Contains([]string{roleRuntime, roleJAR, roleJava, roleEnvironment}, role)
}

func walkDependencies(id string, artefacts map[string]artefact, active, done map[string]bool) error {
	if active[id] {
		return fmt.Errorf("%w: dependency cycle %s", errRecord, id)
	}

	if done[id] {
		return nil
	}

	item, ok := artefacts[id]
	if !ok {
		return fmt.Errorf("%w: missing dependency %s", errRecord, id)
	}

	active[id] = true
	for _, dependency := range item.Dependencies {
		if err := walkDependencies(dependency, artefacts, active, done); err != nil {
			return err
		}
	}

	delete(active, id)
	done[id] = true

	return nil
}

type javaFile struct {
	File fileRef `json:"file"`
	Link *string `json:"link" rule:"text"`
	Mode uint32  `json:"mode" rule:"nonnegative"`
}

type envValue struct {
	Key   string `json:"key"   rule:"text"`
	Value string `json:"value"`
}

type coordinate struct {
	ID         string  `json:"id"          rule:"id"`
	Group      string  `json:"group"       rule:"text"`
	Name       string  `json:"name"        rule:"text"`
	Version    string  `json:"version"     rule:"text"`
	ArtifactID *string `json:"artifact_id" rule:"id"` //nolint:misspell // Required JSON field name.
	POMID      string  `json:"pom_id"      rule:"id"`
}

type environment struct {
	JavaHome     string       `json:"java_home"    rule:"path"`
	JavaVersion  string       `json:"java_version" rule:"text"`
	OS           string       `json:"os"           rule:"text"`
	Arch         string       `json:"arch"         rule:"text"`
	JavaFiles    []javaFile   `json:"java_files"   rule:"nonempty"`
	Variables    []envValue   `json:"variables"`
	Dependencies []coordinate `json:"dependencies" rule:"nonempty"`
}

type lockRecord struct {
	Schema      int          `json:"schema"      rule:"one"`
	ID          string       `json:"id"          rule:"id"`
	Origin      string       `json:"origin"      rule:"https"`
	Revision    string       `json:"revision"    rule:"git"`
	Tree        treeRecord   `json:"tree"`
	Files       []sourceFile `json:"files"       rule:"nonempty"`
	Artefacts   []artefact   `json:"artifacts"   rule:"nonempty"` //nolint:misspell // Required JSON spelling.
	Environment environment  `json:"environment"`
}

func (item *lockRecord) validate() error { return checkLock(item) }

func checkLock(value *lockRecord) error {
	ids := make(map[string]artefact, len(value.Artefacts))
	for _, item := range value.Artefacts {
		ids[item.ID] = item
		if err := checkArtefact(item); err != nil {
			return err
		}
	}

	if err := checkRuntimeClosure(ids); err != nil {
		return err
	}

	for _, dependency := range value.Environment.Dependencies {
		if err := checkCoordinate(dependency, ids); err != nil {
			return err
		}
	}

	return nil
}

type recordValidator interface{ validate() error }

func checkRecord(record any) error {
	validate, exists := record.(recordValidator)
	if exists {
		return validate.validate()
	}

	return nil
}

type includeRef struct {
	Path       string  `json:"path"        rule:"include-path"`
	BlockID    *string `json:"block_id"    ref:"blocks"        rule:"id"`
	ExternalID *string `json:"external_id" ref:"artefact"      rule:"id"`
}

func (item includeRef) validate() error {
	if (item.BlockID == nil) == (item.ExternalID == nil) {
		return fmt.Errorf("%w: include needs exactly one block or external reference", errRecord)
	}

	return nil
}

type blockRecord struct {
	Schema      int          `json:"schema"       rule:"one"`
	ID          string       `json:"id"           rule:"id"`
	Span        span         `json:"span"`
	Kind        string       `json:"kind"         rule:"block-kind"`
	Parent      *string      `json:"parent"       ref:"blocks"      rule:"id"`
	Children    []string     `json:"children"     ref:"blocks"      rule:"ids"`
	IncludeRefs []includeRef `json:"include_refs"`
}

type obligationRecord struct {
	Schema         int      `json:"schema"          rule:"one"`
	ID             string   `json:"id"              rule:"id"`
	BlockIDs       []string `json:"block_ids"       ref:"blocks"                                 rule:"ids,nonempty"`
	Facet          string   `json:"facet"           rule:"text"`
	Statement      string   `json:"statement"       rule:"text"`
	Disposition    string   `json:"disposition"     rule:"enum:behaviour|nonrequirement|pending"`
	RequirementIDs []string `json:"requirement_ids" ref:"requirements"                           rule:"ids"`
	Rationale      *string  `json:"rationale"       rule:"text"`
	ReviewID       *string  `json:"review_id"       ref:"reviews"                                rule:"id"`
}

func (item *obligationRecord) validate() error {
	if item.Disposition == dispositionNonrequirement && (item.Rationale == nil || item.ReviewID == nil) {
		return fmt.Errorf("%w: nonrequirement needs rationale and review", errRecord)
	}

	return nil
}

type requirementRecord struct {
	Schema       int      `json:"schema"       rule:"one"`
	ID           string   `json:"id"           rule:"id"`
	Origin       string   `json:"origin"       rule:"enum:nextflow|wr"`
	Text         string   `json:"text"         rule:"text"`
	Facets       []string `json:"facets"       rule:"texts,nonempty"`
	Dependencies []string `json:"dependencies" ref:"requirements"                             rule:"ids"`
	Interactions []string `json:"interactions" ref:"requirements"                             rule:"ids"`
	Scope        string   `json:"scope"        rule:"enum:required|pending-decision|excluded"`
	DecisionIDs  []string `json:"decision_ids" ref:"decisions"                                rule:"ids"`
	UATIDs       []string `json:"uat_ids"      ref:"uats"                                     rule:"ids"`
	ReviewID     *string  `json:"review_id"    ref:"reviews"                                  rule:"id"`
}

func (item *requirementRecord) validate() error {
	if item.Scope == scopeExcluded && len(item.DecisionIDs) == 0 {
		return fmt.Errorf("%w: exclusion needs decision", errRecord)
	}

	return nil
}

type fixture struct {
	Files     []fileRef `json:"files"     rule:"nonempty"`
	Arguments []string  `json:"arguments"`
}

type observation struct {
	Kind     string  `json:"kind"     rule:"enum:stdout|stderr|exit|file|json"`
	Path     *string `json:"path"     rule:"path"`
	Operator string  `json:"operator" rule:"enum:equals|contains|absent|sha256"`
	Value    string  `json:"value"`
}

func (item observation) validate() error {
	requiresPath := item.Kind == packFile || item.Kind == "json"
	if requiresPath != (item.Path != nil) {
		return fmt.Errorf("%w: observation path inconsistent with kind", errRecord)
	}

	if item.Operator == "sha256" && !validTextRule(item.Value, "hash") {
		return fmt.Errorf("%w: sha256 observation needs hash value", errRecord)
	}

	return nil
}

func validTextRule(text, rule string) bool {
	if pattern, ok := rulePatterns()[rule]; ok {
		return regexp.MustCompile(pattern).MatchString(text)
	}

	if values, ok := ruleEnums()[rule]; ok {
		return slices.Contains(values, text)
	}

	if strings.HasPrefix(rule, "enum:") {
		return slices.Contains(strings.Split(strings.TrimPrefix(rule, "enum:"), "|"), text)
	}

	validators := map[string]func(string) bool{
		textRule: meaningfulText, "path": safeRelative, httpsRule: validHTTPS,
		artefactOriginRule: validArtefactOrigin,
		"package":          validPackage, "time": validTime, "include-path": validIncludePath,
	}
	validate, exists := validators[rule]

	return exists && validate(text)
}

type caseRecord struct {
	ID       string        `json:"id"       rule:"id"`
	Purpose  string        `json:"purpose"  rule:"enum:happy|error|boundary|interaction"`
	Inputs   fixture       `json:"inputs"`
	Expected []observation `json:"expected" rule:"nonempty"`
	FacetID  string        `json:"facet_id" ref:"obligations"                            rule:"id"`
}

type uatRecord struct {
	Schema         int            `json:"schema"          rule:"one"`
	ID             string         `json:"id"              rule:"id"`
	RequirementIDs []string       `json:"requirement_ids" ref:"requirements" rule:"ids,nonempty"`
	FacetIDs       []string       `json:"facet_ids"       ref:"obligations"  rule:"ids,nonempty"`
	Kind           string         `json:"kind"            rule:"uat-kind"`
	Readiness      string         `json:"readiness"       rule:"readiness"`
	Fixture        *fixture       `json:"fixture"`
	Expected       *[]observation `json:"expected"        rule:"nonempty"`
	Cases          []caseRecord   `json:"cases"           rule:"nonempty"`
	Binding        *string        `json:"binding"         ref:"bindings"     rule:"id"`
	TimeoutSeconds int64          `json:"timeout_seconds" rule:"positive"`
	ReviewID       *string        `json:"review_id"       ref:"reviews"      rule:"id"`
}

func (item *uatRecord) validate() error { return checkUAT(item) }

func checkUAT(value *uatRecord) error {
	present := []bool{value.Fixture != nil, value.Expected != nil, value.Binding != nil, value.ReviewID != nil}

	expected := value.Readiness == readinessReady
	for _, exists := range present {
		if exists != expected {
			return fmt.Errorf("%w: UAT readiness contract", errRecord)
		}
	}

	for _, item := range value.Cases {
		if !slices.Contains(value.FacetIDs, item.FacetID) {
			return fmt.Errorf("%w: case facet is absent from UAT facets", errRecord)
		}
	}

	return nil
}

type reviewInputs struct {
	Source        []fileRef `json:"source"        rule:"nonempty"`
	Obligations   []fileRef `json:"obligations"   rule:"nonempty"`
	Requirement   []fileRef `json:"requirement"   rule:"nonempty"`
	UAT           []fileRef `json:"uat"           rule:"nonempty"`
	Normalisation []fileRef `json:"normalization" rule:"nonempty"` //nolint:misspell // Required JSON field name.
	Binding       []fileRef `json:"binding"       rule:"nonempty"`
}

type finding struct {
	ID          string   `json:"id"           rule:"id"`
	Message     string   `json:"message"      rule:"text"`
	AffectedIDs []string `json:"affected_ids" ref:"record" rule:"ids,nonempty"`
}

type reviewRecord struct {
	Schema      int          `json:"schema"       rule:"one"`
	ID          string       `json:"id"           rule:"id"`
	Author      string       `json:"author"       rule:"text"`
	Reviewer    string       `json:"reviewer"     rule:"text"`
	Verdict     string       `json:"verdict"      rule:"enum:accepted|changes-required"`
	InputHashes reviewInputs `json:"input_hashes"`
	SourceSpans []span       `json:"source_spans" rule:"nonempty"`
	Findings    []finding    `json:"findings"`
}

func (item *reviewRecord) validate() error {
	if item.Author == item.Reviewer {
		return fmt.Errorf("%w: author and reviewer must differ", errRecord)
	}

	return nil
}

type decisionRecord struct {
	Schema      int      `json:"schema"       rule:"one"`
	ID          string   `json:"id"           rule:"id"`
	Question    string   `json:"question"     rule:"text"`
	State       string   `json:"state"        rule:"enum:unresolved|resolved"`
	AffectedIDs []string `json:"affected_ids" ref:"record"                    rule:"ids,nonempty"`
	Rationale   string   `json:"rationale"    rule:"text"`
	Resolution  *string  `json:"resolution"   rule:"text"`
	ReviewID    *string  `json:"review_id"    ref:"reviews"                   rule:"id"`
}

func (item *decisionRecord) validate() error { return checkDecision(item) }

func checkDecision(value *decisionRecord) error {
	resolved := value.Resolution != nil && value.ReviewID != nil

	unresolved := value.Resolution == nil && value.ReviewID == nil
	if value.State == decisionResolved && !resolved || value.State == decisionUnresolved && !unresolved {
		return fmt.Errorf("%w: decision resolution and review", errRecord)
	}

	return nil
}

type bindingRecord struct {
	Schema       int       `json:"schema"        rule:"one"`
	ID           string    `json:"id"            rule:"id"`
	UATID        string    `json:"uat_id"        ref:"uats"      rule:"id"`
	Package      string    `json:"package"       rule:"package"`
	Test         string    `json:"test"          rule:"test"`
	SourceFiles  []fileRef `json:"source_files"  rule:"nonempty"`
	EvidenceKind string    `json:"evidence_kind" rule:"uat-kind"`
}

type batchCommand struct {
	Command string  `json:"command" rule:"enum:validate|extract|render|discover|run|verify"`
	Suite   *string `json:"suite"   rule:"enum:foundation-bootstrap|target-inventory|wr-runtime"`
	Check   bool    `json:"check"`
}

func (item batchCommand) validate() error {
	needsSuite := slices.Contains([]string{commandDiscover, commandRun, commandVerify}, item.Command)
	if needsSuite != (item.Suite != nil) {
		return fmt.Errorf("%w: command suite", errRecord)
	}

	if item.Check && item.Command != commandExtract && item.Command != commandRender {
		return fmt.Errorf("%w: command check", errRecord)
	}

	return nil
}

type batchRecord struct {
	Schema      int            `json:"schema"       rule:"one"`
	ID          string         `json:"id"           rule:"id"`
	AssignedIDs []string       `json:"assigned_ids" ref:"record"          rule:"ids,nonempty"`
	DependsOn   []string       `json:"depends_on"   ref:"batches"         rule:"ids"`
	SourceSpans []span         `json:"source_spans" rule:"nonempty"`
	Inputs      []fileRef      `json:"inputs"       rule:"nonempty"`
	Commands    []batchCommand `json:"commands"     rule:"nonempty"`
	Completion  []string       `json:"completion"   rule:"texts,nonempty"`
}

type toolIdentity struct {
	Name    string  `json:"name"    rule:"text"`
	File    fileRef `json:"file"`
	Version string  `json:"version" rule:"text"`
}

type attemptInputs struct {
	GitCommit             string         `json:"git_commit"             rule:"git"`
	DirtyDigest           string         `json:"dirty_digest"           rule:"hash"`
	Target                fileRef        `json:"target"`
	Lock                  fileRef        `json:"lock"`
	SemanticRecords       []fileRef      `json:"semantic_records"       rule:"nonempty"`
	Reviews               []fileRef      `json:"reviews"                rule:"nonempty"`
	Workflows             []fileRef      `json:"workflows"`
	Inputs                []fileRef      `json:"inputs"`
	Expectations          []fileRef      `json:"expectations"`
	Normalisation         []fileRef      `json:"normalization"` //nolint:misspell // Required JSON field name.
	Executables           []fileRef      `json:"executables"            rule:"nonempty"`
	GoSources             []fileRef      `json:"go_sources"             rule:"nonempty"`
	GoMod                 fileRef        `json:"go_mod"`
	GoSum                 fileRef        `json:"go_sum"`
	GoCompiler            toolIdentity   `json:"go_compiler"`
	BuildTags             []string       `json:"build_tags"`
	BuildFlags            []string       `json:"build_flags"`
	OS                    string         `json:"os"                     rule:"text"`
	Arch                  string         `json:"arch"                   rule:"text"`
	Locale                string         `json:"locale"                 rule:"text"`
	Timezone              string         `json:"timezone"               rule:"text"`
	ChildEnvironment      []envValue     `json:"child_environment"`
	NormalisedEnvironment []envValue     `json:"normalized_environment"` //nolint:misspell // Required JSON field name.
	Tools                 []toolIdentity `json:"tools"                  rule:"nonempty"`
	Executor              string         `json:"executor"               rule:"text"`
	Java                  []fileRef      `json:"java"`
	Runtime               []fileRef      `json:"runtime"`
	Dependencies          []fileRef      `json:"dependencies"`
	RunnerVersion         string         `json:"runner_version"         rule:"text"`
	DiscoveryHash         string         `json:"discovery_hash"         rule:"hash"`
}

type observedResult struct {
	UATID        string    `json:"uat_id"        ref:"uats"                                          rule:"id"`
	State        string    `json:"state"         rule:"enum:passed|failed|skipped|timed-out|blocked"`
	EventIndexes []int64   `json:"event_indexes" rule:"indexes,nonempty"`
	Artefacts    []fileRef `json:"artifacts"` //nolint:misspell // Required JSON spelling.
}

type attemptRecord struct {
	Schema    int              `json:"schema"    rule:"one"`
	ID        string           `json:"id"        rule:"id"`
	Suite     string           `json:"suite"     rule:"enum:foundation-bootstrap|target-inventory|wr-runtime"`
	Inputs    attemptInputs    `json:"inputs"`
	Discovery fileRef          `json:"discovery"`
	Events    fileRef          `json:"events"`
	Results   []observedResult `json:"results"`
	Artefacts []fileRef        `json:"artifacts"` //nolint:misspell // Required JSON spelling.
	Exit      int64            `json:"exit"      rule:"nonnegative"`
	Started   string           `json:"started"   rule:"time"`
	Ended     string           `json:"ended"     rule:"time"`
}

func (item *attemptRecord) validate() error {
	started, err := time.Parse(time.RFC3339, item.Started)
	if err != nil {
		return err
	}

	ended, err := time.Parse(time.RFC3339, item.Ended)
	if err != nil {
		return err
	}

	if ended.Before(started) {
		return fmt.Errorf("%w: attempt ended before it started", errRecord)
	}

	return nil
}

func validArtefactOrigin(text string) bool {
	return validHTTPS(text) || regexp.MustCompile(localOriginPattern).MatchString(text)
}

func validHTTPS(text string) bool {
	return regexp.MustCompile(httpsPattern()).MatchString(text)
}

func httpsPattern() string {
	const (
		escaped       = `%[0-9A-Fa-f]{2}`
		unreserved    = `[A-Za-z0-9._~-]`
		subdelims     = `[!$&'()*+,;=]`
		international = `[^\x00-\x7F]`
	)

	component := `(?:` + unreserved + `|` + subdelims + `|` + escaped + `|` + international + `)`
	zone := `(?:%25(?:` + unreserved + `|` + escaped + `)+)?`
	host := `(?:` + component + `+|\[` + ipv6Pattern() + zone + `\])`
	segment := `(?:` + component + `|[:@/])*`
	query := `(?:` + component + `|[:@/?])*`

	return `^https://` + host + `(?::[0-9]*)?(?:/` + segment + `)?(?:\?` + query + `)?$`
}

func ipv6Pattern() string {
	const (
		hex   = `[0-9A-Fa-f]{1,4}`
		octet = `(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])`
	)

	ipv4 := `(?:` + octet + `\.){3}` + octet
	last := `(?:` + hex + `:` + hex + `|` + ipv4 + `)`
	colonHex := `(?:` + hex + `:)`

	// RFC 3986's nine IPv6 alternatives include an optional final IPv4 address.
	alternatives := []string{
		colonHex + `{6}` + last, `::` + colonHex + `{5}` + last,
		`(?:` + hex + `)?::` + colonHex + `{4}` + last,
		`(?:(?:` + hex + `:){0,1}` + hex + `)?::` + colonHex + `{3}` + last,
		`(?:(?:` + hex + `:){0,2}` + hex + `)?::` + colonHex + `{2}` + last,
		`(?:(?:` + hex + `:){0,3}` + hex + `)?::` + colonHex + last,
		`(?:(?:` + hex + `:){0,4}` + hex + `)?::` + last,
		`(?:(?:` + hex + `:){0,5}` + hex + `)?::` + hex,
		`(?:(?:` + hex + `:){0,6}` + hex + `)?::`,
	}

	return `(?:` + strings.Join(alternatives, `|`) + `)`
}

func validTime(text string) bool {
	if !regexp.MustCompile(timePattern()).MatchString(text) {
		return false
	}

	parsed, err := time.Parse(time.RFC3339, text)

	return err == nil && strings.HasSuffix(text, "Z") && parsed.Location() == time.UTC
}

func timePattern() string {
	const (
		day      = `(?:0[1-9]|[12][0-9])`
		leapYear = `(?:[0-9]{2}(?:0[48]|[2468][048]|[13579][26])|(?:[02468][048]|[13579][26])00)`
	)

	date := `(?:[0-9]{4}-(?:(?:01|03|05|07|08|10|12)-(?:` + day + `|3[01])|` +
		`(?:04|06|09|11)-(?:` + day + `|30)|02-(?:0[1-9]|1[0-9]|2[0-8]))|` + leapYear + `-02-29)`

	return `^` + date + `T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?Z$`
}

func decodeRecord(kind string, data []byte) (any, error) {
	raw, err := readRecordJSON(data)
	if err != nil {
		return nil, err
	}

	record := newRecord(kind)
	if record == nil {
		return nil, fmt.Errorf("%w: unknown record %s", errRecord, kind)
	}

	if err = checkShape(reflect.TypeOf(record).Elem(), raw, ""); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err = decoder.Decode(record); err != nil {
		return nil, fmt.Errorf("%w: %w", errRecord, err)
	}

	if err = checkNested(reflect.ValueOf(record)); err != nil {
		return nil, err
	}

	if err = checkRecord(record); err != nil {
		return nil, err
	}

	return record, nil
}

func readRecordJSON(data []byte) (any, error) {
	if !validJSONEnvelope(data) {
		return nil, fmt.Errorf("%w: UTF-8 and final newline required", errRecord)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	raw, err := readJSON(decoder)
	if err != nil {
		return nil, err
	}

	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing JSON", errRecord)
	}

	return raw, nil
}

func validJSONEnvelope(data []byte) bool {
	return utf8.Valid(data) && len(data) > 0 && data[len(data)-1] == '\n'
}

func newRecord(kind string) any {
	typ, exists := recordTypes()[kind]
	if !exists {
		return nil
	}

	return reflect.New(typ).Interface()
}

func checkShape(typ reflect.Type, value any, location string) error {
	if typ.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}

		return checkShape(typ.Elem(), value, location)
	}

	if value == nil {
		return fmt.Errorf("%w: %s cannot be null", errRecord, location)
	}

	switch typ.Kind() {
	case reflect.Struct:
		return checkObject(typ, value, location)
	case reflect.Slice:
		return checkArray(typ, value, location)
	default:
		return nil // encoding/json enforces scalar types without float conversion.
	}
}

func checkObject(typ reflect.Type, value any, location string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: %s must be object", errRecord, location)
	}

	if len(object) != typ.NumField() {
		return fmt.Errorf("%w: %s missing or unknown fields", errRecord, location)
	}

	for i := range typ.NumField() {
		field := typ.Field(i)
		name := field.Tag.Get("json")

		child, exists := object[name]
		if !exists {
			return fmt.Errorf("%w: missing %s.%s", errRecord, location, name)
		}

		if err := checkShape(field.Type, child, location+"."+name); err != nil {
			return err
		}

		if err := checkRules(child, field.Tag.Get("rule")); err != nil {
			return fmt.Errorf("%s.%s: %w", location, name, err)
		}
	}

	return nil
}

func checkArray(typ reflect.Type, value any, location string) error {
	array, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%w: %s must be array", errRecord, location)
	}

	for _, child := range array {
		if err := checkShape(typ.Elem(), child, location); err != nil {
			return err
		}
	}

	return nil
}

func checkRules(value any, rules string) error {
	if value == nil {
		return nil
	}

	for rule := range strings.SplitSeq(rules, ",") {
		if !validRule(value, rule) {
			return fmt.Errorf("%w: constraint %s", errRecord, rule)
		}
	}

	return nil
}

func validRule(value any, rule string) bool {
	if rule == "" {
		return true
	}

	if text, ok := value.(string); ok {
		return validTextRule(text, rule)
	}

	if number, ok := value.(json.Number); ok {
		return validNumberRule(number, rule)
	}

	values, ok := value.([]any)
	if !ok {
		return false
	}

	if rule == "nonempty" {
		return len(values) > 0
	}

	if rule == "indexes" {
		return validIndexes(values)
	}

	return validStringArray(values, rule)
}

func validNumberRule(number json.Number, rule string) bool {
	n, err := number.Int64()
	if err != nil {
		return false
	}

	switch rule {
	case "one":
		return n == 1
	case "positive":
		return n > 0
	case "nonnegative":
		return n >= 0
	default:
		return false
	}
}

func validIndexes(values []any) bool {
	previous := int64(-1)

	for _, value := range values {
		number, ok := value.(json.Number)
		if !ok {
			return false
		}

		index, err := number.Int64()
		if err != nil || index <= previous {
			return false
		}

		previous = index
	}

	return true
}

func validStringArray(values []any, rule string) bool {
	texts := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return false
		}

		texts = append(texts, text)

		elementRule := map[string]string{"ids": "id", "texts": textRule}[rule]
		if !validTextRule(text, elementRule) {
			return false
		}
	}

	return slices.IsSorted(texts) && len(slices.Compact(texts)) == len(values)
}

func nestedKey(value reflect.Value) (string, bool) {
	if value.Kind() != reflect.Struct {
		return "", false
	}

	for _, name := range []string{"ID", "Path", "Key", "UATID", "Name"} {
		field := value.FieldByName(name)
		if field.IsValid() && field.Kind() == reflect.String {
			return field.String(), name == "ID"
		}
	}

	if file := value.FieldByName("File"); file.IsValid() && file.Kind() == reflect.Struct {
		return file.FieldByName("Path").String(), false
	}

	return "", false
}

func duplicateNested(seen map[string]bool, key, previous string, sorted bool) bool {
	return seen[key] || (sorted && key <= previous)
}

func checkNested(value reflect.Value) error {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}

		return checkNested(value.Elem())
	}

	switch value.Kind() {
	case reflect.Struct:
		if err := checkNestedValue(value.Interface()); err != nil {
			return err
		}

		return checkNestedFields(value)
	case reflect.Slice:
		return checkNestedArray(value)
	default:
		return nil
	}
}

func checkNestedValue(value any) error { return checkRecord(value) }

func checkNestedFields(value reflect.Value) error {
	for i := range value.NumField() {
		if err := checkNested(value.Field(i)); err != nil {
			return err
		}
	}

	return nil
}

func checkNestedArray(value reflect.Value) error {
	seen := map[string]bool{}
	previous := ""

	for i := range value.Len() {
		child := value.Index(i)

		key, sorted := nestedKey(child)
		if key != "" {
			if duplicateNested(seen, key, previous, sorted) {
				return fmt.Errorf("%w: duplicate or unsorted nested identity %s", errRecord, key)
			}

			seen[key] = true
			previous = key
		}

		if err := checkNested(child); err != nil {
			return err
		}
	}

	return nil
}

func readJSON(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRecord, err)
	}

	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}

	if delim == '{' {
		return readObject(decoder)
	}

	if delim != '[' {
		return nil, fmt.Errorf("%w: delimiter", errRecord)
	}

	return readArray(decoder)
}

func readObject(decoder *json.Decoder) (any, error) {
	values := map[string]any{}

	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("%w: object key", errRecord)
		}

		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("%w: duplicate key %s", errRecord, name)
		}

		value, err := readJSON(decoder)
		if err != nil {
			return nil, err
		}

		values[name] = value
	}

	_, err := decoder.Token()

	return values, err
}

func readArray(decoder *json.Decoder) (any, error) {
	values := []any{}

	for decoder.More() {
		value, err := readJSON(decoder)
		if err != nil {
			return nil, err
		}

		values = append(values, value)
	}

	_, err := decoder.Token()

	return values, err
}

func validPackage(text string) bool {
	const module = "github.com/VertebrateResequencing/wr"

	return (text == module || strings.HasPrefix(text, module+"/")) && safeRelative(text)
}

func safeRelative(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}

	if strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") {
		return false
	}

	return path.Clean(value) == value && !strings.HasPrefix(value, "../")
}

func rulePatterns() map[string]string {
	return map[string]string{
		"id": `^[A-Z][A-Z0-9_]*$`, "hash": `^[a-f0-9]{64}$`, "git": `^[a-f0-9]{40}$`,
		"test": `^TestUAT_[A-Z0-9_]+$`, "maven": `^[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+:[A-Za-z0-9_.+-]+$`,
	}
}

func ruleEnums() map[string][]string {
	return map[string][]string{
		"readiness": {readinessDraft, readinessReady},
		"uat-kind":  {"foundation", "oracle", "differential", "wr-runtime"},
		"role":      {roleSource, roleLauncher, roleRuntime, roleJAR, roleJava, roleEnvironment, roleMetadata},
		"block-kind": {"heading", "paragraph", "list-item", "table-row", "definition", "code", "directive",
			"grammar-rule", "declaration", "test-case", "trivia", "unclassified"},
	}
}
