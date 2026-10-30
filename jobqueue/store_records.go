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

// This file holds the log store's record types and their encoding (spec
// "Records"). Every record is [u8 type][u64 seq] then its body; keys are the
// 16 binary bytes of a job's 32-hex-digit key.

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
)

const (
	// logRecordHeader is the length of every record's type and seq.
	logRecordHeader = 1 + 8

	// logKeyLen is the length of a key in a record, and logKeyHexLen that of
	// a job's key.
	logKeyLen    = 16
	logKeyHexLen = 2 * logKeyLen

	// stdOut and stdErr are a std record's stream byte.
	stdOut uint8 = 0
	stdErr uint8 = 1

	// limitRemovedFlag is a limit record's removed byte for a removed group.
	limitRemovedFlag uint8 = 1
)

// errLogRecordMalformed is the error of parsing a record that is not one the
// log store writes.
var errLogRecordMalformed = errors.New("malformed log store record")

// logRecordType is the first byte of every log store record.
type logRecordType uint8

const (
	logRecSpec     logRecordType = 1  // key, flat spec
	logRecState    logRecordType = 2  // key, flat run state
	logRecDelete   logRecordType = 3  // key
	logRecModify   logRecordType = 4  // old key, new key
	logRecStd      logRecordType = 5  // key, u8 stream, bytes
	logRecEnv      logRecordType = 6  // env key, blob
	logRecLimit    logRecordType = 7  // group, u8 removed, i64 limit
	logRecRGLookup logRecordType = 8  // rep group, key
	logRecComplete logRecordType = 9  // key, flat run state
	logRecDepGroup logRecordType = 10 // dep group name
	logRecExitStat logRecordType = 11 // key, jobExitData's sample fields
)

// logRecord is a parsed record. Its byte slices and strings alias the record
// it was parsed from.
type logRecord struct {
	typ  logRecordType
	seq  uint64
	key  logKey
	body []byte // what follows the key, or the header if the type has no key

	// set by parseLogRecord for the types that have them.
	stream  uint8  // std
	name    string // env key, limit group, rep group, dep group name
	limit   int64  // limit
	removed bool   // limit
}

// logRecordParsers parse the body of each type of record the log store reads.
//
//nolint:gochecknoglobals // a constant table
var logRecordParsers = [...]func(r *logRecord, body []byte) error{
	logRecSpec:     (*logRecord).takeKey,
	logRecState:    (*logRecord).takeKey,
	logRecDelete:   (*logRecord).takeKey,
	logRecStd:      (*logRecord).parseStd,
	logRecEnv:      (*logRecord).parseEnv,
	logRecLimit:    (*logRecord).parseLimit,
	logRecRGLookup: (*logRecord).parseRGLookup,
	logRecComplete: (*logRecord).takeKey,
	logRecDepGroup: (*logRecord).parseDepGroup,
	logRecExitStat: (*logRecord).takeKey,
}

// logOp is one record as the reducer applies it: its type, seq and Ref, and
// what of its body the in-memory state keeps.
type logOp struct {
	typ  logRecordType
	seq  uint64
	key  logKey
	ref  wal.Ref
	name string // env key, limit group, rep group, dep group name
	blob []byte // env

	index     logSpecIndex // spec
	handedOut bool         // state: liveRecordHandedOut of it
	stream    uint8        // std
	hasStd    bool         // std: not empty
	limit     int64        // limit
	removed   bool         // limit
}

// logState is the log store's in-memory state (spec "In-memory state of
// logStore"), which the reducer, apply, changes.
type logState struct {
	live          map[logKey]*logLive
	history       map[logKey]*logHistory
	envs          map[string][]byte
	limits        map[string]int64
	depGroupsSeen map[string]struct{}
	repGroupKeys  map[string]map[logKey]struct{} // rglookup records
	reverseDeps   map[string]map[logKey]struct{} // dep group to keys depending on it
	maxSeq        uint64
}

// logReducers apply each type of record that changes the in-memory state; an
// exitstat record changes none.
//
//nolint:gochecknoglobals // a constant table
var logReducers = [...]func(st *logState, op *logOp){
	logRecSpec:     (*logState).applySpec,
	logRecState:    (*logState).applyState,
	logRecDelete:   (*logState).applyDelete,
	logRecStd:      (*logState).applyStd,
	logRecEnv:      (*logState).applyEnv,
	logRecLimit:    (*logState).applyLimit,
	logRecRGLookup: (*logState).applyRGLookup,
	logRecComplete: (*logState).applyComplete,
	logRecDepGroup: (*logState).applyDepGroup,
}

// appendKeyedRecord appends a record of typ with key and body to dst.
func appendKeyedRecord(dst []byte, typ logRecordType, seq uint64, key logKey, body []byte) []byte {
	dst = appendRecordHeader(dst, typ, seq)
	dst = append(dst, key[:]...)

	return append(dst, body...)
}

// appendRecordHeader appends a record's type and seq to dst.
func appendRecordHeader(dst []byte, typ logRecordType, seq uint64) []byte {
	dst = append(dst, byte(typ))

	return binary.LittleEndian.AppendUint64(dst, seq)
}

// logKey is a job key as records hold it.
type logKey [logKeyLen]byte

// parseLogKey returns key, a job's 32-hex-digit key, as a logKey.
func parseLogKey(key string) (logKey, error) {
	var k logKey

	if len(key) != logKeyHexLen {
		return k, fmt.Errorf("%w: key %q is not %d hex digits", errLogRecordMalformed, key, logKeyHexLen)
	}

	if _, err := hex.Decode(k[:], []byte(key)); err != nil {
		return k, fmt.Errorf("%w: key %q: %w", errLogRecordMalformed, key, err)
	}

	return k, nil
}

// String returns k as a job's key.
func (k logKey) String() string {
	return hex.EncodeToString(k[:])
}

// appendStdRecord appends a std record of key's stream to dst; an empty std
// clears the stream.
func appendStdRecord(dst []byte, seq uint64, key logKey, stream uint8, std []byte) []byte {
	dst = appendRecordHeader(dst, logRecStd, seq)
	dst = append(dst, key[:]...)
	dst = append(dst, stream)

	return append(dst, std...)
}

// appendRGLookupRecord appends an rglookup record of key under repGroup to dst.
func appendRGLookupRecord(dst []byte, seq uint64, repGroup string, key logKey) []byte {
	dst = appendRecordHeader(dst, logRecRGLookup, seq)
	dst = appendFlatStrings(dst, repGroup)

	return append(dst, key[:]...)
}

// appendExitStatRecord appends key's exitstat record, holding exit's sample
// fields, to dst.
func appendExitStatRecord(dst []byte, seq uint64, key logKey, exit *jobExitData) []byte {
	dst = appendRecordHeader(dst, logRecExitStat, seq)
	dst = append(dst, key[:]...)
	dst = appendFlatStrings(dst, exit.reqGroup, exit.failReason)

	for _, v := range [...]int64{int64(exit.peakRAM), int64(exit.requiredRAM), exit.peakDisk, int64(exit.secs)} {
		dst = binary.AppendVarint(dst, v)
	}

	return dst
}

// takeKey sets r's key from the start of body and its body to the rest.
func (r *logRecord) takeKey(body []byte) error {
	if len(body) < logKeyLen {
		return fmt.Errorf("%w: type %d without a key", errLogRecordMalformed, r.typ)
	}

	r.key = logKey(body[:logKeyLen])
	r.body = body[logKeyLen:]

	return nil
}

func (r *logRecord) parseRGLookup(body []byte) error {
	fr := flatReader{b: body}
	r.name = fr.str()

	key := fr.span(logKeyLen)
	if err := fr.end(); err != nil {
		return err
	}

	r.key = logKey(key)

	return nil
}

// unindex removes key from the reverse dep group index entries of index.
func (st *logState) unindex(key logKey, index logSpecIndex) {
	for _, dg := range index.depDeps {
		keys := st.reverseDeps[dg]
		delete(keys, key)

		if len(keys) == 0 {
			delete(st.reverseDeps, dg)
		}
	}
}

// stdRefs returns the Refs of a key's std streams, and which it has.
func (st *logState) stdRefs(k logKey) ([2]wal.Ref, [2]bool) {
	e := st.live[k]
	if e == nil {
		return [2]wal.Ref{}, [2]bool{}
	}

	return e.std, e.hasStd
}

// parseLogRecord parses rec.
func parseLogRecord(rec []byte) (logRecord, error) {
	if len(rec) < logRecordHeader {
		return logRecord{}, fmt.Errorf("%w: %d bytes", errLogRecordMalformed, len(rec))
	}

	r := logRecord{typ: logRecordType(rec[0]), seq: binary.LittleEndian.Uint64(rec[1:])}

	if int(r.typ) >= len(logRecordParsers) || logRecordParsers[r.typ] == nil {
		return r, fmt.Errorf("%w: type %d", errLogRecordMalformed, r.typ)
	}

	return r, logRecordParsers[r.typ](&r, rec[logRecordHeader:])
}

func (r *logRecord) parseDepGroup(body []byte) error {
	r.name = string(body)

	return nil
}

func (r *logRecord) parseStd(body []byte) error {
	if err := r.takeKey(body); err != nil {
		return err
	}

	if len(r.body) == 0 || r.body[0] > stdErr {
		return fmt.Errorf("%w: std record without a stream", errLogRecordMalformed)
	}

	r.stream = r.body[0]
	r.body = r.body[1:]

	return nil
}

func (r *logRecord) parseEnv(body []byte) error {
	fr := flatReader{b: body}
	r.name = fr.str()
	r.body = fr.span(fr.left())

	return fr.end()
}

func (r *logRecord) parseLimit(body []byte) error {
	fr := flatReader{b: body}
	r.name = fr.str()
	r.removed = fr.u8() == limitRemovedFlag
	r.limit = fr.varint()

	return fr.end()
}

// logSpecIndex is what of a spec record the in-memory indexes hold.
type logSpecIndex struct {
	repGroup string
	depDeps  []string // Dependencies.DepGroups(): the reverse dep group index
}

// newLogSpecIndex returns job's index fields. The caller holds job's read lock,
// or job is not shared; the strings may alias a record job was decoded from.
func newLogSpecIndex(job *Job) logSpecIndex {
	return logSpecIndex{repGroup: job.RepGroup, depDeps: job.Dependencies.DepGroups()}
}

// cloned returns i with strings that alias nothing.
func (i logSpecIndex) cloned() logSpecIndex {
	deps := make([]string, len(i.depDeps))
	for n, d := range i.depDeps {
		deps[n] = strings.Clone(d)
	}

	return logSpecIndex{repGroup: strings.Clone(i.repGroup), depDeps: deps}
}

// apply applies ops, in order, to the store's state.
func (s *logStore) apply(ops []logOp) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range ops {
		s.state.apply(&ops[i])
	}
}

// apply is the reducer (spec "Records"): it applies op to the state by the seq
// rules, so the order ops are applied in does not matter.
func (st *logState) apply(op *logOp) {
	st.maxSeq = max(st.maxSeq, op.seq)

	if int(op.typ) < len(logReducers) && logReducers[op.typ] != nil {
		logReducers[op.typ](st, op)
	}
}

// applyEnv stores an env's blob.
func (st *logState) applyEnv(op *logOp) {
	st.envs[op.name] = op.blob
}

// applyRGLookup records a key under a rep group.
func (st *logState) applyRGLookup(op *logOp) {
	addToIndex(st.repGroupKeys, op.name, op.key)
}

// addToIndex adds key to index's set for name.
func addToIndex(index map[string]map[logKey]struct{}, name string, key logKey) {
	keys := index[name]
	if keys == nil {
		keys = make(map[logKey]struct{})
		index[name] = keys
	}

	keys[key] = struct{}{}
}

// applyDepGroup records a dep group as seen.
func (st *logState) applyDepGroup(op *logOp) {
	st.depGroupsSeen[op.name] = struct{}{}
}

// applySpec makes the key live with op's spec, unless a later spec is live. It
// drops the key's state and std records earlier than op and keeps later ones,
// which outrank it, so a state or std record outranks a spec it is later than
// whichever applies first.
func (st *logState) applySpec(op *logOp) {
	e := st.live[op.key]
	if e == nil {
		e = &logLive{}
		st.live[op.key] = e
	} else if op.seq <= e.specSeq {
		return
	}

	e.specSeq, e.spec, e.index = op.seq, op.ref, op.index
	e.dropBefore(op.seq)

	for _, dg := range op.index.depDeps {
		addToIndex(st.reverseDeps, dg, op.key)
	}
}

// applyState sets a live key's state, if op is later than its spec and state.
func (st *logState) applyState(op *logOp) {
	e := st.live[op.key]
	if e == nil || op.seq <= e.specSeq || op.seq <= e.stateSeq {
		return
	}

	e.stateSeq, e.state, e.handedOut = op.seq, op.ref, op.handedOut
}

// applyDelete removes a live key, with its state and std, unless its spec is
// later than op.
func (st *logState) applyDelete(op *logOp) {
	e := st.live[op.key]
	if e == nil || op.seq <= e.specSeq {
		return
	}

	delete(st.live, op.key)

	if _, complete := st.history[op.key]; !complete {
		st.unindex(op.key, e.index)
	}
}

// applyStd sets or, if empty, clears a live key's std stream, if op is later
// than its spec and that stream's last std.
func (st *logState) applyStd(op *logOp) {
	e := st.live[op.key]
	if e == nil || op.seq <= e.specSeq || op.seq <= e.stdSeq[op.stream] {
		return
	}

	e.stdSeq[op.stream], e.std[op.stream], e.hasStd[op.stream] = op.seq, op.ref, op.hasStd
}

// applyComplete makes op the key's history entry, with the key's live spec,
// unless a later complete record applied. It drops a live key's std. A key
// with neither a live spec nor a history entry has no spec to complete, so op
// is dropped, as an orphan state record is.
func (st *logState) applyComplete(op *logOp) {
	h := st.history[op.key]
	if h != nil && op.seq <= h.seq {
		return
	}

	e := st.live[op.key]

	switch {
	case e != nil:
		st.history[op.key] = &logHistory{seq: op.seq, complete: op.ref, spec: e.spec, repGroup: e.index.repGroup}

		for stream := range e.stdSeq {
			e.stdSeq[stream] = max(e.stdSeq[stream], op.seq)
			e.hasStd[stream] = false
		}
	case h != nil:
		h.seq, h.complete = op.seq, op.ref
	}
}

// applyLimit sets or removes a limit group's limit.
func (st *logState) applyLimit(op *logOp) {
	if op.removed {
		delete(st.limits, op.name)

		return
	}

	st.limits[op.name] = op.limit
}

// logLive is a live key's in-memory state.
type logLive struct {
	specSeq, stateSeq uint64
	stdSeq            [2]uint64
	spec, state       wal.Ref
	std               [2]wal.Ref
	hasStd            [2]bool
	handedOut         bool
	index             logSpecIndex
}

// dropBefore forgets e's state, and each std stream, whose last applied record
// is earlier than seq.
func (e *logLive) dropBefore(seq uint64) {
	if e.stateSeq < seq {
		e.stateSeq, e.handedOut = 0, false
	}

	for stream := range e.stdSeq {
		if e.stdSeq[stream] < seq {
			e.stdSeq[stream], e.std[stream], e.hasStd[stream] = 0, wal.Ref{}, false
		}
	}
}

// logHistory is a key's history entry: its last applied complete record and
// the spec record current when it applied.
type logHistory struct {
	seq            uint64
	complete, spec wal.Ref
	repGroup       string
}

func newLogState() logState {
	return logState{
		live:          make(map[logKey]*logLive),
		history:       make(map[logKey]*logHistory),
		envs:          make(map[string][]byte),
		limits:        make(map[string]int64),
		depGroupsSeen: make(map[string]struct{}),
		repGroupKeys:  make(map[string]map[logKey]struct{}),
		reverseDeps:   make(map[string]map[logKey]struct{}),
	}
}

// limitBytes returns group's limit as the bbolt store keeps it, for
// planLimitGroup, or nil if it has none.
func (st *logState) limitBytes(group string) []byte {
	limit, stored := st.limits[group]
	if !stored {
		return nil
	}

	return binary.BigEndian.AppendUint64(nil, uint64(limit)) //nolint:gosec // limits stored are never negative
}

// logFrames builds the frames of one write: its records, each framed, and the
// ops that apply them, in the same order. Its records are in groups that a
// split into blocks never separates (see group).
type logFrames struct {
	frames []byte
	ops    []logOp
	ends   []int  // where each op's frame ends in frames
	groups []int  // the first op of each group after the first
	rec    []byte // scratch for the record being framed
}

// group starts a new group with the next record added: the records added since
// the last group started stay in one block, so a crash or a failed batch never
// leaves some of them durable without the rest (a job's spec without its
// state).
func (f *logFrames) group() {
	if n := len(f.ops); n > 0 && (len(f.groups) == 0 || f.groups[len(f.groups)-1] != n) {
		f.groups = append(f.groups, n)
	}
}

// add frames rec, the record op applies, which it copies.
func (f *logFrames) add(rec []byte, op logOp) {
	f.frames = wal.AppendFrame(f.frames, rec)
	f.ops = append(f.ops, op)
	f.ends = append(f.ends, len(f.frames))
	f.rec = rec[:0]
}

// chunks splits f's ops into runs of whole groups whose frames fit in limit
// bytes, or of one group whose frames alone do not, returning each run's first
// op and the op after its last.
func (f *logFrames) chunks(limit int) [][2]int {
	var (
		chunks [][2]int
		start  int
	)

	for i, first := range f.groups {
		end := len(f.ops)
		if i+1 < len(f.groups) {
			end = f.groups[i+1]
		}

		if f.ends[end-1]-f.start(start) > limit {
			chunks = append(chunks, [2]int{start, first})
			start = first
		}
	}

	return append(chunks, [2]int{start, len(f.ops)})
}

// start returns where op i's frame starts in frames.
func (f *logFrames) start(i int) int {
	if i == 0 {
		return 0
	}

	return f.ends[i-1]
}

// blocks returns chunks' frames, each as one block compressed with s2, and how
// many records each holds.
func (f *logFrames) blocks(chunks [][2]int) ([][]byte, []int, error) {
	blocks := make([][]byte, len(chunks))
	ns := make([]int, len(chunks))

	for i, c := range chunks {
		block, err := wal.AppendBlock(nil, wal.CodecS2, f.frames[f.start(c[0]):f.ends[c[1]-1]])
		if err != nil {
			return nil, nil, err
		}

		blocks[i], ns[i] = block, c[1]-c[0]
	}

	return blocks, ns, nil
}

// appendEnvRecord appends an env record of envkey's blob to dst.
func appendEnvRecord(dst []byte, seq uint64, envkey string, blob []byte) []byte {
	dst = appendRecordHeader(dst, logRecEnv, seq)
	dst = appendFlatStrings(dst, envkey)

	return append(dst, blob...)
}

// appendLimitRecord appends a limit record of group to dst: its limit, or that
// it was removed.
func appendLimitRecord(dst []byte, seq uint64, group string, limit int64, removed bool) []byte {
	dst = appendRecordHeader(dst, logRecLimit, seq)
	dst = appendFlatStrings(dst, group)

	var flag uint8
	if removed {
		flag = limitRemovedFlag
	}

	dst = append(dst, flag)

	return binary.AppendVarint(dst, limit)
}

// appendDepGroupRecord appends a depgroup record of name to dst.
func appendDepGroupRecord(dst []byte, seq uint64, name string) []byte {
	dst = appendRecordHeader(dst, logRecDepGroup, seq)

	return append(dst, name...)
}
