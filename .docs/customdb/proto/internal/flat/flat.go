// Package flat is a prototype hand-written binary codec for wr's Job, split
// into an immutable spec (written once, at add) and a small mutable run state
// (written at every transition). Encoding appends into a caller buffer and
// allocates nothing once the buffer is big enough; decoding points strings
// into the source bytes (zero-copy), so it allocates only for slices that the
// destination Job does not already have capacity for.
//
// Rarely-set nested fields (Dependencies, Behaviours, MountConfigs) are kept
// as an opaque Binc blob, encoded only when non-empty: the hot path never
// pays for them, and their shape can change without touching this format.
package flat

import (
	"encoding/binary"
	"errors"
	"time"
	"unsafe"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/ugorji/go/codec"
)

// SpecVersion is the first byte of every encoded spec.
const SpecVersion = 1

// ErrShort is returned when a record ends early.
var ErrShort = errors.New("flat: record too short")

var le = binary.LittleEndian //nolint:gochecknoglobals

// AppendSpec appends the immutable part of j to dst.
func AppendSpec(dst []byte, j *jobqueue.Job, blob []byte) []byte {
	dst = append(dst, SpecVersion)
	dst = appendStr(dst, j.Cmd)
	dst = appendStr(dst, j.Cwd)
	dst = appendStr(dst, j.RepGroup)
	dst = appendStr(dst, j.ReqGroup)
	dst = appendStr(dst, j.Group)
	dst = appendStr(dst, j.EnvKey)
	dst = appendStr(dst, j.BsubMode)
	dst = appendStr(dst, j.MonitorDocker)
	dst = appendStr(dst, j.WithDocker)
	dst = appendStr(dst, j.WithSingularity)
	dst = appendStr(dst, j.ContainerMounts)
	dst = append(dst, flags(j.CwdMatters, j.ChangeHome, j.ContainerImageUser), j.Override, j.Priority, j.Retries)
	dst = le.AppendUint64(dst, uint64(j.NoRetriesOverWalltime))
	dst = le.AppendUint64(dst, j.BsubID)
	dst = appendReq(dst, j.Requirements)
	dst = appendStrs(dst, j.LimitGroups)
	dst = appendStrs(dst, j.LimitGroupsForDisplay)
	dst = appendStrs(dst, j.Modules)
	dst = appendStrs(dst, j.DepGroups)
	dst = binary.AppendUvarint(dst, uint64(len(blob)))

	return append(dst, blob...)
}

// Blob Binc-encodes the rarely-set nested fields of j, or returns nil when they
// are all empty (the common case, which then costs nothing).
func Blob(h *codec.BincHandle, j *jobqueue.Job) []byte {
	if len(j.Dependencies) == 0 && len(j.Behaviours) == 0 && len(j.MountConfigs) == 0 {
		return nil
	}

	var out []byte

	b := nested{j.Dependencies, j.Behaviours, j.MountConfigs}
	if err := codec.NewEncoderBytes(&out, h).Encode(&b); err != nil {
		panic(err)
	}

	return out
}

type nested struct {
	D jobqueue.Dependencies
	B jobqueue.Behaviours
	M jobqueue.MountConfigs
}

// DecodeSpec fills the spec fields of j from src. Strings alias src, so src
// must outlive j (callers keep one arena per record or per segment). It
// returns the nested blob, which the caller decodes only if non-nil.
func DecodeSpec(src []byte, j *jobqueue.Job) ([]byte, error) {
	r := reader{b: src}
	if r.u8() != SpecVersion {
		return nil, errors.New("flat: bad spec version")
	}

	j.Cmd = r.str()
	j.Cwd = r.str()
	j.RepGroup = r.str()
	j.ReqGroup = r.str()
	j.Group = r.str()
	j.EnvKey = r.str()
	j.BsubMode = r.str()
	j.MonitorDocker = r.str()
	j.WithDocker = r.str()
	j.WithSingularity = r.str()
	j.ContainerMounts = r.str()
	f := r.u8()
	j.CwdMatters, j.ChangeHome, j.ContainerImageUser = f&1 != 0, f&2 != 0, f&4 != 0
	j.Override, j.Priority, j.Retries = r.u8(), r.u8(), r.u8()
	j.NoRetriesOverWalltime = time.Duration(r.u64())
	j.BsubID = r.u64()
	j.Requirements = r.req(j.Requirements)
	j.LimitGroups = r.strs(j.LimitGroups)
	j.LimitGroupsForDisplay = r.strs(j.LimitGroupsForDisplay)
	j.Modules = r.strs(j.Modules)
	j.DepGroups = r.strs(j.DepGroups)
	blob := r.bytes()

	if r.bad {
		return nil, ErrShort
	}

	return blob, nil
}

func flags(bs ...bool) byte {
	var f byte

	for i, b := range bs {
		if b {
			f |= 1 << i
		}
	}

	return f
}

func appendStr(dst []byte, s string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(s)))

	return append(dst, s...)
}

func appendStrs(dst []byte, ss []string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(ss)))
	for _, s := range ss {
		dst = appendStr(dst, s)
	}

	return dst
}

func appendReq(dst []byte, r *scheduler.Requirements) []byte {
	if r == nil {
		return append(dst, 0)
	}

	dst = append(dst, 1, flags(r.CoresSet, r.DiskSet, r.OtherSet))
	dst = le.AppendUint64(dst, uint64(r.RAM))
	dst = le.AppendUint64(dst, uint64(r.Time))
	dst = le.AppendUint64(dst, uint64(r.Cores*1000))
	dst = le.AppendUint64(dst, uint64(r.Disk))
	dst = binary.AppendUvarint(dst, uint64(len(r.Other)))

	for k, v := range r.Other {
		dst = appendStr(dst, k)
		dst = appendStr(dst, v)
	}

	return dst
}

type reader struct {
	b   []byte
	off int
	bad bool
}

func (r *reader) need(n int) bool {
	if r.bad || r.off+n > len(r.b) {
		r.bad = true

		return false
	}

	return true
}

func (r *reader) u8() byte {
	if !r.need(1) {
		return 0
	}

	r.off++

	return r.b[r.off-1]
}

func (r *reader) u64() uint64 {
	if !r.need(8) {
		return 0
	}

	r.off += 8

	return le.Uint64(r.b[r.off-8:])
}

func (r *reader) uvarint() int {
	if r.bad {
		return 0
	}

	v, n := binary.Uvarint(r.b[r.off:])
	if n <= 0 {
		r.bad = true

		return 0
	}

	r.off += n

	return int(v)
}

func (r *reader) bytes() []byte {
	n := r.uvarint()
	if n == 0 || !r.need(n) {
		return nil
	}

	r.off += n

	return r.b[r.off-n : r.off : r.off]
}

func (r *reader) str() string {
	b := r.bytes()
	if len(b) == 0 {
		return ""
	}

	return unsafe.String(&b[0], len(b))
}

func (r *reader) strs(reuse []string) []string {
	n := r.uvarint()
	if n == 0 {
		return reuse[:0]
	}

	out := reuse[:0]
	for range n {
		out = append(out, r.str())
	}

	return out
}

func (r *reader) req(reuse *scheduler.Requirements) *scheduler.Requirements {
	if r.u8() == 0 {
		return nil
	}

	if reuse == nil {
		reuse = &scheduler.Requirements{}
	}

	f := r.u8()
	reuse.CoresSet, reuse.DiskSet, reuse.OtherSet = f&1 != 0, f&2 != 0, f&4 != 0
	reuse.RAM = int(r.u64())
	reuse.Time = time.Duration(r.u64())
	reuse.Cores = float64(r.u64()) / 1000
	reuse.Disk = int(r.u64())

	n := r.uvarint()
	if n == 0 {
		clear(reuse.Other)

		return reuse
	}

	if reuse.Other == nil {
		reuse.Other = make(map[string]string, n)
	}

	for range n {
		k := r.str()
		reuse.Other[k] = r.str()
	}

	return reuse
}
