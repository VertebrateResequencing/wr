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

// This file holds the log store's flat codec: a Job split into its spec and
// its run state (logRunState), each a hand-written, versioned, variable-length
// binary record that encodes into a caller's buffer and decodes without
// allocating. The run state is every jobRunState field plus the persisted
// fields a transition changes after the add without writing a new spec; the
// spec is every other field encodeJob persists, fixed from the add until a
// modify or put-back writes a new spec.
//
// Both records decode to exactly what bbolt's Binc encoding of the same Job
// decodes to: times keep Binc's whole-minute UTC offset, and nil and empty
// slices and maps stay distinct. Dependencies, Behaviours and MountConfigs,
// whose shapes are nested (Behaviour.Arg is an any), are one embedded Binc
// blob, present only when one of them is non-nil.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/gofrs/uuid/v5"
	"github.com/ugorji/go/codec"
)

// flatVersion is the first byte of every flat spec and run state this wr
// writes; a decoder refuses a higher one.
const flatVersion byte = 1

// Flag bits of a flat spec's flags byte.
const (
	flatCwdMatters byte = 1 << iota
	flatChangeHome
	flatContainerImageUser
	flatEnvCRetrieved
)

// Flag bits of a flat run state's flags byte.
const (
	flatExited byte = 1 << iota
	flatLost
	flatRerunAfterRun
)

// Flag bits of a flat Requirements' flags byte.
const (
	flatCoresSet byte = 1 << iota
	flatDiskSet
	flatOtherSet
)

// flatReqsPresent marks a non-nil *scheduler.Requirements.
const flatReqsPresent byte = 1

// secondsPerMinute converts Binc's whole-minute zone offsets.
const secondsPerMinute = 60

var (
	// errStoreVersionTooNew is the error of reading a store, or a record in
	// one, written by a newer wr.
	errStoreVersionTooNew = errors.New("store written by a newer wr; use a newer wr")

	// errFlatMalformed is the error of decoding bytes that are not a whole
	// flat spec or run state.
	errFlatMalformed = errors.New("malformed flat record")
)

// flatZone is a fixed zone decoded times are in, with its offset in minutes.
type flatZone struct {
	minutes int64
	loc     *time.Location
}

// lastFlatZone caches the last non-UTC zone a flat time decoded to, so that
// decoding a run of times in one zone allocates no Location.
var lastFlatZone atomic.Pointer[flatZone] //nolint:gochecknoglobals // a lock-free cache

// logRunState is a flat run state: every jobRunState field, plus the persisted
// Job fields that a transition (release, bury, kick, dependency resolution)
// changes after the add without writing a new spec, so that they survive a
// restart as bbolt's whole-job writes keep them today. jobRunState itself is
// left as it is: bbolt's run-state records are its Binc encoding, and an older
// record without these fields would zero them on recovery.
type logRunState struct {
	jobRunState
	UntilBuried         uint8
	FailBumpsCleared    uint8
	ModifiedReqs        *scheduler.Requirements
	FailedRunReqs       *scheduler.Requirements
	WaitingForDepGroups []string
}

// newLogRunState returns j's run state, deep-copying its Requirements and
// WaitingForDepGroups. The caller holds j's read lock.
func newLogRunState(j *Job) logRunState {
	return logRunState{
		jobRunState:         newJobRunState(j),
		UntilBuried:         j.UntilBuried,
		FailBumpsCleared:    j.FailBumpsCleared,
		ModifiedReqs:        cloneRequirements(j.ModifiedReqs),
		FailedRunReqs:       cloneRequirements(j.FailedRunReqs),
		WaitingForDepGroups: slices.Clone(j.WaitingForDepGroups),
	}
}

// applyTo sets every field of rs on j, as jobRunState.applyTo does. The caller
// holds j's lock, or j is not yet shared.
func (rs *logRunState) applyTo(j *Job) {
	j.UntilBuried = rs.UntilBuried
	j.FailBumpsCleared = rs.FailBumpsCleared
	j.ModifiedReqs = rs.ModifiedReqs
	j.FailedRunReqs = rs.FailedRunReqs
	j.WaitingForDepGroups = rs.WaitingForDepGroups

	rs.jobRunState.applyTo(j)
}

// decodeFlatRunState sets every field of rs from src. Strings and byte slices
// in rs alias src, so src must not change while rs, or a Job it is applied to,
// is in use. Requirements and WaitingForDepGroups are always new, so rs may be
// applied to a Job and then decoded into again; without them it allocates
// nothing.
func decodeFlatRunState(src []byte, rs *logRunState) error {
	r := flatReader{b: src}
	if err := r.version(); err != nil {
		return err
	}

	r.runStateFields(rs)
	rs.Requirements, rs.RequirementsOrig = r.reqs(), r.reqs()
	rs.ModifiedReqs, rs.FailedRunReqs = r.reqs(), r.reqs()
	rs.WaitingForDepGroups = r.stringSlice(nil)

	return r.end()
}

// appendFlatRunState appends rs's flat run state to dst: every logRunState
// field. It allocates nothing when dst has room.
func appendFlatRunState(dst []byte, rs *logRunState) []byte {
	dst = append(dst, flatVersion)
	dst = appendFlatStrings(dst, string(rs.State), rs.FailReason, rs.Host,
		rs.HostID, rs.HostIP, rs.ActualCwd)
	dst = append(dst, flatFlags(rs.Exited, rs.Lost, rs.RerunAfterRun))

	for _, n := range [...]int64{int64(rs.Exitcode), int64(rs.Pid), int64(rs.RunnerPid),
		int64(rs.PeakRAM), rs.PeakDisk, int64(rs.CPUtime), int64(rs.DelayTime)} {
		dst = binary.AppendVarint(dst, n)
	}

	dst = append(dst, rs.UntilBuried, rs.FailBumpsCleared)
	dst = binary.AppendUvarint(dst, rs.RunnerReservation)
	dst = binary.AppendUvarint(dst, uint64(rs.Attempts))
	dst = appendFlatTime(dst, rs.StartTime)
	dst = appendFlatTime(dst, rs.EndTime)
	dst = appendFlatBytes(dst, rs.StdOutC)
	dst = appendFlatBytes(dst, rs.StdErrC)
	dst = append(dst, rs.ReservedBy[:]...)
	dst = appendFlatReqs(dst, rs.Requirements)
	dst = appendFlatReqs(dst, rs.RequirementsOrig)
	dst = appendFlatReqs(dst, rs.ModifiedReqs)
	dst = appendFlatReqs(dst, rs.FailedRunReqs)

	return appendFlatStringSlice(dst, rs.WaitingForDepGroups)
}

// appendFlatStrings appends each of ss, length-prefixed.
func appendFlatStrings(dst []byte, ss ...string) []byte {
	for _, s := range ss {
		dst = binary.AppendUvarint(dst, uint64(len(s)))
		dst = append(dst, s...)
	}

	return dst
}

// flatFlags packs bs into a byte, bs[i] at bit i.
func flatFlags(bs ...bool) byte {
	var f byte

	for i, b := range bs {
		if b {
			f |= 1 << i
		}
	}

	return f
}

// appendFlatTime appends t as Binc keeps it: Unix seconds, nanoseconds and
// its zone's offset in whole minutes (0 for UTC).
func appendFlatTime(dst []byte, t time.Time) []byte {
	dst = binary.AppendVarint(dst, t.Unix())
	dst = binary.AppendUvarint(dst, uint64(t.Nanosecond())) //nolint:gosec // in [0, 1e9)

	var minutes int64

	if t.Location() != time.UTC {
		_, offset := t.Zone()
		minutes = bincZoneMinutes(offset)
	}

	return binary.AppendVarint(dst, minutes)
}

// bincZoneMinutes returns a zone offset in seconds as Binc keeps it: whole
// minutes, wrapped to a signed 14-bit number.
func bincZoneMinutes(offset int) int64 {
	const spareBits = 2 // the top 2 of Binc's 16 zone bits hold a DST flag

	return int64(int16(uint16(offset/secondsPerMinute)<<spareBits) >> spareBits) //nolint:gosec // wraps as Binc does
}

// appendFlatBytes appends b as its length plus 1 (0 for nil), then b.
func appendFlatBytes(dst, b []byte) []byte {
	if b == nil {
		return append(dst, 0)
	}

	dst = binary.AppendUvarint(dst, uint64(len(b))+1)

	return append(dst, b...)
}

// appendFlatReqs appends r, or a 0 for nil. Its Other map is a count plus 1 (0
// for nil) then key and value pairs, in map order.
func appendFlatReqs(dst []byte, r *scheduler.Requirements) []byte {
	if r == nil {
		return append(dst, 0)
	}

	dst = append(dst, flatReqsPresent, flatFlags(r.CoresSet, r.DiskSet, r.OtherSet))
	dst = binary.AppendVarint(dst, int64(r.RAM))
	dst = binary.AppendVarint(dst, int64(r.Time))
	dst = binary.LittleEndian.AppendUint64(dst, math.Float64bits(r.Cores))
	dst = binary.AppendVarint(dst, int64(r.Disk))

	if r.Other == nil {
		return append(dst, 0)
	}

	dst = binary.AppendUvarint(dst, uint64(len(r.Other))+1)

	for k, v := range r.Other {
		dst = appendFlatStrings(dst, k, v)
	}

	return dst
}

// appendFlatStringSlice appends ss as its length plus 1 (0 for nil), then
// each string.
func appendFlatStringSlice(dst []byte, ss []string) []byte {
	if ss == nil {
		return append(dst, 0)
	}

	dst = binary.AppendUvarint(dst, uint64(len(ss))+1)

	return appendFlatStrings(dst, ss...)
}

// flatBlob is the Binc-encoded part of a flat spec.
type flatBlob struct {
	Dependencies Dependencies
	Behaviours   Behaviours
	MountConfigs MountConfigs
}

// appendFlatBlob appends j's Dependencies, Behaviours and MountConfigs as a
// length-prefixed Binc blob, or a 0 length when all are nil.
func appendFlatBlob(dst []byte, j *Job, ch codec.Handle) ([]byte, error) {
	if j.Dependencies == nil && j.Behaviours == nil && j.MountConfigs == nil {
		return append(dst, 0), nil
	}

	var blob []byte

	err := codec.NewEncoderBytes(&blob, ch).Encode(&flatBlob{
		Dependencies: j.Dependencies,
		Behaviours:   j.Behaviours,
		MountConfigs: j.MountConfigs,
	})
	if err != nil {
		return dst, err
	}

	return appendFlatBytes(dst, blob), nil
}

// flatReader reads a flat record. Its first failed read marks it bad, after
// which every read returns a zero value.
type flatReader struct {
	b   []byte
	off int
	bad bool
}

// runStateFields sets rs's fields, all but its Requirements and
// WaitingForDepGroups, from the record.
func (r *flatReader) runStateFields(rs *logRunState) {
	var state string

	r.strings(&state, &rs.FailReason, &rs.Host, &rs.HostID, &rs.HostIP, &rs.ActualCwd)
	rs.State = JobState(state)

	flags := r.u8()
	rs.Exited, rs.Lost, rs.RerunAfterRun = flags&flatExited != 0, flags&flatLost != 0, flags&flatRerunAfterRun != 0
	rs.Exitcode, rs.Pid, rs.RunnerPid, rs.PeakRAM = int(r.varint()), int(r.varint()), int(r.varint()), int(r.varint())
	rs.PeakDisk = r.varint()
	rs.CPUtime, rs.DelayTime = time.Duration(r.varint()), time.Duration(r.varint())
	rs.UntilBuried, rs.FailBumpsCleared = r.u8(), r.u8()
	rs.RunnerReservation = r.uvarint()
	rs.Attempts = r.u32()
	rs.StartTime, rs.EndTime = r.time(), r.time()
	rs.StdOutC, rs.StdErrC = r.bytes(), r.bytes()
	r.uuid(&rs.ReservedBy)
}

// version reads the record's version byte, returning an error unless it is
// flatVersion.
func (r *flatReader) version() error {
	switch v := r.u8(); {
	case r.bad || v == 0:
		return errFlatMalformed
	case v > flatVersion:
		return fmt.Errorf("flat record version %d: %w", v, errStoreVersionTooNew)
	default:
		return nil
	}
}

// end returns an error unless every read succeeded and consumed the record.
func (r *flatReader) end() error {
	if r.bad || r.off != len(r.b) {
		return errFlatMalformed
	}

	return nil
}

// left returns how many bytes of the record are unread.
func (r *flatReader) left() uint64 {
	return uint64(len(r.b) - r.off) //nolint:gosec // off never passes len(b)
}

// need reports whether n more bytes remain, marking r bad if not.
func (r *flatReader) need(n uint64) bool {
	if r.bad || n > r.left() {
		r.bad = true

		return false
	}

	return true
}

func (r *flatReader) u8() byte {
	if !r.need(1) {
		return 0
	}

	r.off++

	return r.b[r.off-1]
}

func (r *flatReader) u32() uint32 {
	v := r.uvarint()
	if v > math.MaxUint32 {
		r.bad = true

		return 0
	}

	return uint32(v)
}

func (r *flatReader) uvarint() uint64 {
	if r.bad {
		return 0
	}

	v, n := binary.Uvarint(r.b[r.off:])
	if n <= 0 {
		r.bad = true

		return 0
	}

	r.off += n

	return v
}

func (r *flatReader) varint() int64 {
	if r.bad {
		return 0
	}

	v, n := binary.Varint(r.b[r.off:])
	if n <= 0 {
		r.bad = true

		return 0
	}

	r.off += n

	return v
}

// span returns the next n bytes of the record, capped so an append to them
// copies.
func (r *flatReader) span(n uint64) []byte {
	if !r.need(n) {
		return nil
	}

	start := r.off
	r.off += int(n) //nolint:gosec // n is at most left()

	return r.b[start:r.off:r.off]
}

// str returns the next length-prefixed string, aliasing the record.
func (r *flatReader) str() string {
	b := r.span(r.uvarint())
	if len(b) == 0 {
		return ""
	}

	return unsafe.String(&b[0], len(b))
}

// strings sets each of dsts to the next string.
func (r *flatReader) strings(dsts ...*string) {
	for _, dst := range dsts {
		*dst = r.str()
	}
}

// count reads a length plus 1, returning the length and whether it is non-nil.
// Each counted item takes at least minBytes, so a count the record cannot
// hold is bad.
func (r *flatReader) count(minBytes uint64) (uint64, bool) {
	c := r.uvarint()
	if c == 0 || r.bad {
		return 0, false
	}

	n := c - 1
	if n > r.left()/minBytes {
		r.bad = true

		return 0, false
	}

	return n, true
}

// stringSlice returns the next string slice, reusing reuse's backing array.
func (r *flatReader) stringSlice(reuse []string) []string {
	n, ok := r.count(1)
	if !ok {
		return nil
	}

	out := reuse[:0]
	if out == nil {
		out = make([]string, 0, n)
	}

	for range n {
		out = append(out, r.str())
	}

	return out
}

// bytes returns the next byte slice, aliasing the record.
func (r *flatReader) bytes() []byte {
	n, ok := r.count(1)
	if !ok {
		return nil
	}

	return r.span(n)
}

// uuid reads the next 16 bytes into u.
func (r *flatReader) uuid(u *uuid.UUID) {
	copy(u[:], r.span(uint64(len(u))))
}

// reqs returns the next Requirements, newly made. A tag other than 0 (nil)
// or flatReqsPresent marks the record bad.
func (r *flatReader) reqs() *scheduler.Requirements {
	switch tag := r.u8(); {
	case r.bad || tag == 0:
		return nil
	case tag != flatReqsPresent:
		r.bad = true

		return nil
	}

	req := &scheduler.Requirements{}

	flags := r.u8()
	req.CoresSet, req.DiskSet = flags&flatCoresSet != 0, flags&flatDiskSet != 0
	req.OtherSet = flags&flatOtherSet != 0
	req.RAM = int(r.varint())
	req.Time = time.Duration(r.varint())

	if cores := r.span(8); cores != nil { //nolint:mnd // a float64's width
		req.Cores = math.Float64frombits(binary.LittleEndian.Uint64(cores))
	}

	req.Disk = int(r.varint())
	req.Other = r.stringMap()

	return req
}

// stringMap returns the next string map, newly made.
func (r *flatReader) stringMap() map[string]string {
	n, ok := r.count(2) //nolint:mnd // a key and a value
	if !ok {
		return nil
	}

	m := make(map[string]string, n)

	for range n {
		k := r.str()
		m[k] = r.str()
	}

	return m
}

// time returns the next time, as Binc decodes it: UTC for a 0 offset, else
// in an unnamed zone of that offset.
func (r *flatReader) time() time.Time {
	sec, nsec, minutes := r.varint(), r.uvarint(), r.varint()
	if r.bad || bincZoneMinutes(int(minutes*secondsPerMinute)) != minutes {
		r.bad = true

		return time.Time{}
	}

	// time.Unix carries nanoseconds outside [0, 1s) into the seconds, so they
	// fail this round trip.
	t := time.Unix(sec, int64(nsec)) //nolint:gosec // a wrapped value fails the round trip
	if t.Unix() != sec {
		r.bad = true

		return time.Time{}
	}

	if minutes == 0 {
		return t.UTC()
	}

	return t.In(flatZoneOf(minutes))
}

// flatZoneOf returns the unnamed fixed zone minutes from UTC, from the cache
// when it last decoded the same zone.
func flatZoneOf(minutes int64) *time.Location {
	if z := lastFlatZone.Load(); z != nil && z.minutes == minutes {
		return z.loc
	}

	z := &flatZone{minutes: minutes, loc: time.FixedZone("", int(minutes*secondsPerMinute))}
	lastFlatZone.Store(z)

	return z.loc
}

// decodeBlob sets j's Dependencies, Behaviours and MountConfigs from the
// record's blob, all nil without one, then checks the record is consumed.
func (r *flatReader) decodeBlob(j *Job, ch codec.Handle) error {
	blob := r.bytes()
	if err := r.end(); err != nil {
		return err
	}

	if blob == nil {
		j.Dependencies, j.Behaviours, j.MountConfigs = nil, nil, nil

		return nil
	}

	var b flatBlob

	if err := codec.NewDecoderBytes(blob, ch).Decode(&b); err != nil {
		return errors.Join(errFlatMalformed, err)
	}

	if err := checkStoredElements(b.Dependencies, b.Behaviours); err != nil {
		return errors.Join(errFlatMalformed, err)
	}

	j.Dependencies, j.Behaviours, j.MountConfigs = b.Dependencies, b.Behaviours, b.MountConfigs

	return nil
}

// specFields sets j's spec fields, all but its blob ones, from the record.
func (r *flatReader) specFields(j *Job) {
	r.strings(&j.Cmd, &j.Cwd, &j.RepGroup, &j.ReqGroup, &j.Group,
		&j.BsubMode, &j.MonitorDocker, &j.WithDocker, &j.WithSingularity,
		&j.ContainerMounts, &j.EnvKey, &j.Queue)

	flags := r.u8()
	j.CwdMatters, j.ChangeHome = flags&flatCwdMatters != 0, flags&flatChangeHome != 0
	j.ContainerImageUser, j.EnvCRetrieved = flags&flatContainerImageUser != 0, flags&flatEnvCRetrieved != 0
	j.Override, j.Priority, j.Retries = r.u8(), r.u8(), r.u8()
	j.NoRetriesOverWalltime = time.Duration(r.varint())
	j.BsubID = r.uvarint()
	j.Similar = int(r.varint())

	for _, ss := range [...]*[]string{&j.LimitGroups, &j.LimitGroupsForDisplay,
		&j.Modules, &j.DepGroups} {
		*ss = r.stringSlice(*ss)
	}

	j.EnvC = r.bytes()
	j.EnvOverride = r.bytes()
	j.ReadyTime = r.time()
}

// decodeFlatSpec sets j's spec fields, the ones appendFlatSpec writes, from
// src, then drops cleanup behaviours j cannot honour, as decodeJob does. It
// leaves j's run-state fields alone.
//
// Strings and byte slices in j alias src, so src must not change while j is in
// use; j's own string slices are reused, so the caller must not share them.
// With those reused and no blob, it allocates nothing.
func decodeFlatSpec(src []byte, j *Job, ch codec.Handle) error {
	r := flatReader{b: src}
	if err := r.version(); err != nil {
		return err
	}

	r.specFields(j)

	if err := r.decodeBlob(j, ch); err != nil {
		return err
	}

	j.dropImpossibleCleanups()

	return nil
}

// appendFlatSpec appends j's flat spec to dst: every field encodeJob persists
// that logRunState does not. It allocates nothing when dst has room and
// Dependencies, Behaviours and MountConfigs are all nil; otherwise it encodes
// those with ch. The caller holds j's read lock, or j is not yet shared.
func appendFlatSpec(dst []byte, j *Job, ch codec.Handle) ([]byte, error) {
	dst = append(dst, flatVersion)
	dst = appendFlatStrings(dst, j.Cmd, j.Cwd, j.RepGroup, j.ReqGroup, j.Group,
		j.BsubMode, j.MonitorDocker, j.WithDocker, j.WithSingularity,
		j.ContainerMounts, j.EnvKey, j.Queue)
	dst = append(dst, flatFlags(j.CwdMatters, j.ChangeHome, j.ContainerImageUser, j.EnvCRetrieved),
		j.Override, j.Priority, j.Retries)
	dst = binary.AppendVarint(dst, int64(j.NoRetriesOverWalltime))
	dst = binary.AppendUvarint(dst, j.BsubID)
	dst = binary.AppendVarint(dst, int64(j.Similar))

	for _, ss := range [...][]string{j.LimitGroups, j.LimitGroupsForDisplay,
		j.Modules, j.DepGroups} {
		dst = appendFlatStringSlice(dst, ss)
	}

	dst = appendFlatBytes(dst, j.EnvC)
	dst = appendFlatBytes(dst, j.EnvOverride)
	dst = appendFlatTime(dst, j.ReadyTime)

	return appendFlatBlob(dst, j, ch)
}
