package flat

import (
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/jobgen"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/gofrs/uuid/v5"
	"github.com/ugorji/go/codec"
)

// runState mirrors develop's jobRunState (db_runstate.go), the record a
// reservation and start write today.
type runState struct {
	State             jobqueue.JobState
	Exited            bool
	Exitcode          int
	FailReason        string
	Lost              bool
	Pid               int
	RunnerPid         int
	Host              string
	HostID            string
	HostIP            string
	ActualCwd         string
	StartTime         time.Time
	EndTime           time.Time
	PeakRAM           int
	PeakDisk          int64
	CPUtime           time.Duration
	StdOutC           []byte
	StdErrC           []byte
	ReservedBy        uuid.UUID
	RunnerReservation uint64
	Attempts          uint32
	DelayTime         time.Duration
	Requirements      *scheduler.Requirements
	RequirementsOrig  *scheduler.Requirements
	RerunAfterRun     bool
}

func TestSpecRoundTrip(t *testing.T) {
	j := jobgen.Job(7, 10000)
	enc := AppendSpec(nil, j, nil)

	var got jobqueue.Job
	if _, err := DecodeSpec(enc, &got); err != nil {
		t.Fatal(err)
	}

	if got.Cmd != j.Cmd || got.RepGroup != j.RepGroup || got.Requirements.RAM != 1500 ||
		got.LimitGroups[0] != "portal" || got.DepGroups[0] != j.DepGroups[0] || got.Retries != 3 {
		t.Fatalf("mismatch %q", got.Cmd[:20])
	}

	for i := range len(enc) {
		var g jobqueue.Job
		if _, err := DecodeSpec(enc[:i], &g); err == nil {
			t.Fatalf("truncation at %d not detected", i)
		}
	}

	t.Logf("spec bytes: %d (cmd %d)", len(enc), len(j.Cmd))
}

func TestStateRoundTrip(t *testing.T) {
	s := State{State: SRunning, Attempts: 2, Pid: 1234, StartTime: time.Now().UnixNano(), Exitcode: -1}
	s.SetHost("node-13-16")

	buf := make([]byte, StateSize)
	PutState(buf, &s)

	var g State
	if err := GetState(buf, &g); err != nil || g != s {
		t.Fatalf("mismatch %v %+v", err, g)
	}

	buf[100] ^= 1
	if err := GetState(buf, &g); err == nil {
		t.Fatal("corruption not detected")
	}
}

func benchJob(b *testing.B, size int) *jobqueue.Job {
	b.Helper()

	return jobgen.Job(1, size)
}

func BenchmarkBincEncode1K(b *testing.B)  { benchBincEncode(b, 1300) }
func BenchmarkBincEncode10K(b *testing.B) { benchBincEncode(b, 10000) }
func BenchmarkBincDecode1K(b *testing.B)  { benchBincDecode(b, 1300) }
func BenchmarkBincDecode10K(b *testing.B) { benchBincDecode(b, 10000) }
func BenchmarkFlatEncode1K(b *testing.B)  { benchFlatEncode(b, 1300) }
func BenchmarkFlatEncode10K(b *testing.B) { benchFlatEncode(b, 10000) }
func BenchmarkFlatDecode1K(b *testing.B)  { benchFlatDecode(b, 1300, false) }
func BenchmarkFlatDecode10K(b *testing.B) { benchFlatDecode(b, 10000, false) }

// a fresh Job per decode plus a private copy of the record, as recovery
// building long-lived jobs would do.
func BenchmarkFlatDecodeNewJob10K(b *testing.B) { benchFlatDecode(b, 10000, true) }

func benchBincEncode(b *testing.B, size int) {
	j := benchJob(b, size)
	h := new(codec.BincHandle)
	enc := codec.NewEncoderBytes(nil, h)

	var out []byte

	b.ReportAllocs()

	for b.Loop() {
		out = out[:0]
		enc.ResetBytes(&out)

		if err := enc.Encode(j); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportMetric(float64(len(out)), "bytes")
}

func benchBincDecode(b *testing.B, size int) {
	j := benchJob(b, size)
	h := new(codec.BincHandle)

	var enc []byte
	if err := codec.NewEncoderBytes(&enc, h).Encode(j); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()

	for b.Loop() {
		var g jobqueue.Job
		if err := codec.NewDecoderBytes(enc, h).Decode(&g); err != nil {
			b.Fatal(err)
		}
	}
}

func benchFlatEncode(b *testing.B, size int) {
	j := benchJob(b, size)
	buf := make([]byte, 0, 64<<10)

	b.ReportAllocs()

	for b.Loop() {
		buf = AppendSpec(buf[:0], j, nil)
	}

	b.ReportMetric(float64(len(buf)), "bytes")
}

func benchFlatDecode(b *testing.B, size int, fresh bool) {
	enc := AppendSpec(nil, benchJob(b, size), nil)

	var g jobqueue.Job

	b.ReportAllocs()

	for b.Loop() {
		src := enc
		dst := &g

		if fresh {
			src = append([]byte(nil), enc...)
			dst = &jobqueue.Job{}
		}

		if _, err := DecodeSpec(src, dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStatePut(b *testing.B) {
	s := State{State: SRunning, Attempts: 2, Pid: 1234, StartTime: time.Now().UnixNano()}
	s.SetHost("node-13-16")
	buf := make([]byte, StateSize)

	b.ReportAllocs()

	for b.Loop() {
		PutState(buf, &s)
	}
}

func BenchmarkStateGet(b *testing.B) {
	s := State{State: SRunning, Attempts: 2, Pid: 1234}
	buf := make([]byte, StateSize)
	PutState(buf, &s)

	var g State

	b.ReportAllocs()

	for b.Loop() {
		if err := GetState(buf, &g); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBincRunState is develop's run-state encode for comparison.
func BenchmarkBincRunState(b *testing.B) {
	h := new(codec.BincHandle)
	rs := runState{State: jobqueue.JobStateRunning, Pid: 1234, Host: "node-13-16", StartTime: time.Now(),
		Requirements: &scheduler.Requirements{RAM: 1500, Time: time.Hour, Cores: 1}}
	enc := codec.NewEncoderBytes(nil, h)

	var out []byte

	b.ReportAllocs()

	for b.Loop() {
		out = out[:0]
		enc.ResetBytes(&out)

		if err := enc.Encode(&rs); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportMetric(float64(len(out)), "bytes")
}
