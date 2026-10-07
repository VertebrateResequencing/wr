package flat

import (
	"encoding/hex"
	"errors"
	"hash/crc32"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/gofrs/uuid/v5"
)

// StateSize is the fixed size of an encoded run state. Every field a
// reservation, start, release, bury or archive changes fits, so a transition
// is one fixed-size record: a slot overwrite or a log append of 192 bytes,
// against 1-10KB to re-encode the whole job.
const StateSize = 192

const hostMax = StateSize - 144

// ErrBadCRC says a state record is torn or corrupt.
var ErrBadCRC = errors.New("flat: state record CRC mismatch")

var crcTable = crc32.MakeTable(crc32.Castagnoli) //nolint:gochecknoglobals

// Key is a job key in binary (wr keys are 32 hex digits of an md5).
type Key [16]byte

// KeyOf converts a wr hex key.
func KeyOf(hexKey string) Key {
	var k Key

	_, _ = hex.Decode(k[:], []byte(hexKey))

	return k
}

// State is the mutable per-run part of a job, as a value type with no
// pointers, so a table of them costs the GC nothing to scan.
type State struct {
	Key               Key
	Seq               uint64
	State             uint8
	Exited            bool
	Lost              bool
	RerunAfterRun     bool
	UntilBuried       uint8
	FailCode          uint8
	Attempts          uint32
	Exitcode          int32
	Pid               uint32
	RunnerPid         uint32
	ReservedBy        uuid.UUID
	RunnerReservation uint64
	StartTime         int64
	EndTime           int64
	CPUtime           int64
	PeakRAM           uint64
	PeakDisk          uint64
	DelayTime         int64
	ReqRAM            uint32
	ReqTimeSec        uint32
	ReqDisk           uint32
	ReqCoresMilli     uint32
	SpecOff           uint64 // where the job's spec record starts (slot design)
	HostLen           uint8
	Host              [hostMax]byte
}

// States maps wr's JobState strings to compact codes.
var States = []jobqueue.JobState{ //nolint:gochecknoglobals
	"", jobqueue.JobStateNew, jobqueue.JobStateDelayed, jobqueue.JobStateReady,
	jobqueue.JobStateReserved, jobqueue.JobStateRunning, jobqueue.JobStateLost,
	jobqueue.JobStateBuried, jobqueue.JobStateDependent, jobqueue.JobStateSuspended,
	jobqueue.JobStateComplete, jobqueue.JobStateDeleted,
}

// Compact state codes used by the prototypes.
const (
	SReady    = 3
	SReserved = 4
	SRunning  = 5
	SBuried   = 7
	SComplete = 10
	SDeleted  = 11
)

// SetHost stores h, truncated to what fits.
func (s *State) SetHost(h string) {
	s.HostLen = uint8(copy(s.Host[:], h))
}

// PutState encodes s into dst[:StateSize] with a CRC, allocating nothing.
func PutState(dst []byte, s *State) {
	_ = dst[StateSize-1]
	copy(dst[4:20], s.Key[:])
	le.PutUint64(dst[20:], s.Seq)
	dst[28] = s.State
	dst[29] = flags(s.Exited, s.Lost, s.RerunAfterRun)
	dst[30] = s.UntilBuried
	dst[31] = s.FailCode
	le.PutUint32(dst[32:], s.Attempts)
	le.PutUint32(dst[36:], uint32(s.Exitcode))
	le.PutUint32(dst[40:], s.Pid)
	le.PutUint32(dst[44:], s.RunnerPid)
	copy(dst[48:64], s.ReservedBy[:])
	le.PutUint64(dst[64:], s.RunnerReservation)
	le.PutUint64(dst[72:], uint64(s.StartTime))
	le.PutUint64(dst[80:], uint64(s.EndTime))
	le.PutUint64(dst[88:], uint64(s.CPUtime))
	le.PutUint64(dst[96:], s.PeakRAM)
	le.PutUint64(dst[104:], s.PeakDisk)
	le.PutUint64(dst[112:], uint64(s.DelayTime))
	le.PutUint32(dst[120:], s.ReqRAM)
	le.PutUint32(dst[124:], s.ReqTimeSec)
	le.PutUint32(dst[128:], s.ReqDisk)
	dst[132] = s.HostLen
	dst[133], dst[134], dst[135] = byte(s.ReqCoresMilli), byte(s.ReqCoresMilli>>8), byte(s.ReqCoresMilli>>16)
	le.PutUint64(dst[136:], s.SpecOff)
	copy(dst[144:StateSize], s.Host[:])
	le.PutUint32(dst, crc32.Checksum(dst[4:StateSize], crcTable))
}

// GetState decodes src[:StateSize] into s, allocating nothing.
func GetState(src []byte, s *State) error {
	if len(src) < StateSize {
		return ErrShort
	}

	if le.Uint32(src) != crc32.Checksum(src[4:StateSize], crcTable) {
		return ErrBadCRC
	}

	copy(s.Key[:], src[4:20])
	s.Seq = le.Uint64(src[20:])
	s.State = src[28]
	f := src[29]
	s.Exited, s.Lost, s.RerunAfterRun = f&1 != 0, f&2 != 0, f&4 != 0
	s.UntilBuried = src[30]
	s.FailCode = src[31]
	s.Attempts = le.Uint32(src[32:])
	s.Exitcode = int32(le.Uint32(src[36:]))
	s.Pid = le.Uint32(src[40:])
	s.RunnerPid = le.Uint32(src[44:])
	copy(s.ReservedBy[:], src[48:64])
	s.RunnerReservation = le.Uint64(src[64:])
	s.StartTime = int64(le.Uint64(src[72:]))
	s.EndTime = int64(le.Uint64(src[80:]))
	s.CPUtime = int64(le.Uint64(src[88:]))
	s.PeakRAM = le.Uint64(src[96:])
	s.PeakDisk = le.Uint64(src[104:])
	s.DelayTime = int64(le.Uint64(src[112:]))
	s.ReqRAM = le.Uint32(src[120:])
	s.ReqTimeSec = le.Uint32(src[124:])
	s.ReqDisk = le.Uint32(src[128:])
	s.HostLen = src[132]
	s.ReqCoresMilli = uint32(src[133]) | uint32(src[134])<<8 | uint32(src[135])<<16
	s.SpecOff = le.Uint64(src[136:])
	copy(s.Host[:], src[144:StateSize])

	return nil
}

// ApplyTo copies s onto the run fields of j (the host string is the only
// allocation, and only when it changed).
func (s *State) ApplyTo(j *jobqueue.Job) {
	j.State = States[s.State]
	j.Exited, j.Lost, j.RerunAfterRun = s.Exited, s.Lost, s.RerunAfterRun
	j.UntilBuried = s.UntilBuried
	j.Attempts = s.Attempts
	j.Exitcode = int(s.Exitcode)
	j.Pid, j.RunnerPid = int(s.Pid), int(s.RunnerPid)
	j.ReservedBy = s.ReservedBy
	j.RunnerReservation = s.RunnerReservation
	j.StartTime = time.Unix(0, s.StartTime)
	j.EndTime = time.Unix(0, s.EndTime)
	j.CPUtime = time.Duration(s.CPUtime)
	j.PeakRAM, j.PeakDisk = int(s.PeakRAM), int64(s.PeakDisk)
	j.DelayTime = time.Duration(s.DelayTime)

	if h := s.Host[:s.HostLen]; j.Host != string(h) {
		j.Host = string(h)
	}
}
