// Command crashtest checks a design's recovery claims. It starts itself as a
// child that hammers the store with reserve/start/archive transitions and
// records each one in an ack file only after its write was reported durable,
// kills that child (SIGKILL to its exact pid) at a random moment, recovers the
// store and requires every acknowledged transition to be there. It then
// truncates and corrupts copies of the logs at random points and requires
// recovery to succeed with a prefix.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/jobgen"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/store"
	"github.com/VertebrateResequencing/wr/jobqueue"
)

func main() {
	design := flag.String("design", "wal", "design")
	dir := flag.String("dir", "", "base directory")
	iters := flag.Int("iters", 10, "kill/recover rounds")
	child := flag.Bool("child", false, "internal: be the writer")
	flag.Parse()

	if *child {
		if err := writer(*design, *dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		return
	}

	bad := 0

	for i := range *iters {
		d := filepath.Join(*dir, fmt.Sprintf("%s-%d", *design, i))
		_ = os.RemoveAll(d)

		v, err := round(*design, d)
		if err != nil {
			fmt.Println("round error:", err)

			bad++

			continue
		}

		fmt.Println(v)

		if len(v) > 4 && v[:4] == "FAIL" {
			bad++
		}

		_ = os.RemoveAll(d)
	}

	fmt.Printf("CRASHTEST design=%s rounds=%d failed=%d\n", *design, *iters, bad)
}

func round(design, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	cmd := exec.Command(os.Args[0], "-child", "-design", design, "-dir", dir) //nolint:gosec
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return "", err
	}

	time.Sleep(1500*time.Millisecond + rand.N(3*time.Second))

	if err := cmd.Process.Kill(); err != nil { // SIGKILL to exactly this child
		return "", err
	}

	_ = cmd.Wait()

	acks, err := readAcks(filepath.Join(dir, "acks"))
	if err != nil {
		return "", err
	}

	got, torn, err := store.RecoverStates(design, filepath.Join(dir, "store"))
	if err != nil {
		return "", err
	}

	missing := 0

	for k, seq := range acks {
		if got[k].Seq < seq {
			missing++
		}
	}

	verdict := "PASS"
	if missing > 0 {
		verdict = "FAIL"
	}

	trunc := truncations(design, filepath.Join(dir, "store"))

	return fmt.Sprintf("%s acked=%d recovered=%d lostAcked=%d tornTailBytes=%d %s",
		verdict, len(acks), len(got), missing, torn, trunc), nil
}

func readAcks(path string) (map[flat.Key]uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	out := make(map[flat.Key]uint64)

	for len(b) >= 24 {
		var k flat.Key
		copy(k[:], b[:16])

		if s := binary.LittleEndian.Uint64(b[16:24]); s > out[k] {
			out[k] = s
		}

		b = b[24:]
	}

	return out, nil
}

// truncations cuts and corrupts copies of the main log and checks recovery
// still succeeds and never yields more than the intact log did.
func truncations(design, dir string) string {
	var main string

	switch design {
	case "wal", "keyfiles":
		main = "b000.log"
	case "slots":
		main = "history.log"
	default:
		return ""
	}

	src := filepath.Join(dir, main)

	st, err := os.Stat(src)
	if err != nil || st.Size() == 0 {
		return "trunc=skipped"
	}

	full, _, _ := store.RecoverStates(design, dir)
	fails := 0

	for i := range 20 {
		cp := dir + fmt.Sprintf(".t%d", i)
		_ = os.RemoveAll(cp)
		_ = copyDir(dir, cp)

		p := filepath.Join(cp, main)
		cut := rand.Int64N(st.Size())

		if i%2 == 0 {
			_ = os.Truncate(p, cut)
		} else {
			flip(p, cut)
		}

		got, _, err := store.RecoverStates(design, cp)
		if err != nil || len(got) > len(full) {
			fails++
		}

		_ = os.RemoveAll(cp)
	}

	return fmt.Sprintf("truncOrFlipFails=%d/20", fails)
}

func flip(path string, off int64) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer f.Close()

	b := []byte{0}
	_, _ = f.ReadAt(b, off)
	b[0] ^= 0xff
	_, _ = f.WriteAt(b, off)
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}

	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, e := range ents {
		in, err := os.Open(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}

		out, err := os.Create(filepath.Join(dst, e.Name()))
		if err != nil {
			in.Close()

			return err
		}

		_, _ = io.Copy(out, in)
		in.Close()
		out.Close()
	}

	return nil
}

// writer is the child: 300 runners each taking jobs through
// reserve(seq 3a+1), start(3a+2), archive(3a+3), acking after durability.
func writer(design, dir string) error {
	s, err := store.Open(design, filepath.Join(dir, "store"), false)
	if err != nil {
		return err
	}

	ack, err := os.OpenFile(filepath.Join(dir, "acks"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}

	const njobs = 3000

	jobs := make([]*jobqueue.Job, njobs)
	keys := make([]flat.Key, njobs)

	for i := range njobs {
		jobs[i] = jobgen.Job(i, 2000)
		keys[i] = flat.KeyOf(jobgen.Key(jobs[i]))
	}

	for c := 0; c < njobs; c += 500 {
		if err := s.Add(jobs[c:c+500], keys[c:c+500]); err != nil {
			return err
		}
	}

	var wg sync.WaitGroup

	for r := range 300 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			buf := make([]byte, 24)

			for attempt := uint64(0); ; attempt++ {
				for i := r; i < njobs; i += 300 {
					for phase, kind := range []store.Kind{store.Reserve, store.Start, store.Archive} {
						st := flat.State{Key: keys[i], Seq: attempt*3 + uint64(phase) + 1, State: flat.SReserved + uint8(phase)}
						if kind == store.Archive {
							st.State = flat.SReady // keep it live so it can run again
						}

						done, errf := s.Put(kind, &st, jobs[i])
						<-done

						if errf() != nil {
							return
						}

						copy(buf, keys[i][:])
						binary.LittleEndian.PutUint64(buf[16:], st.Seq)
						_, _ = ack.Write(buf)
					}
				}
			}
		}()
	}

	wg.Wait()

	return nil
}
