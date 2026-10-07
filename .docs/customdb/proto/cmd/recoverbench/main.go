// Command recoverbench builds a store holding N live jobs (5% of them with a
// reserved or running state, as at a crash under load), evicts its files from
// the page cache, and times recovery: reading everything back and decoding
// every live job into a jobqueue.Job, as the manager's startup does.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/jobgen"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/store"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"golang.org/x/sys/unix"
)

func main() {
	design := flag.String("design", "wal", "design")
	dir := flag.String("dir", "", "store directory")
	n := flag.Int("n", 120000, "live jobs")
	cmd := flag.Int("cmd", 10000, "command bytes")
	build := flag.Bool("build", true, "build the store first")
	flag.Parse()

	if *build {
		if err := buildStore(*design, *dir, *n, *cmd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	var size int64

	ents, _ := os.ReadDir(*dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil {
			size += info.Size()
		}
	}

	modes := []string{*design}
	if *design == "slots" {
		modes = append(modes, "slotslazy")
	}

	for _, m := range modes {
		for _, temp := range []string{"cold", "warm"} {
			if temp == "cold" {
				evict(*dir)
			}

			r, err := store.Recover(m, *dir, true)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}

			fmt.Printf("RECOVER design=%s fs=%s n=%d cmd=%d files=%d sizeMB=%.0f %s %s\n",
				m, filepath.Dir(*dir), *n, *cmd, len(ents), float64(size)/1e6, temp, r)
		}
	}
}

func buildStore(design, dir string, n, cmd int) error {
	t := time.Now()

	s, err := store.Open(design, dir, true)
	if err != nil {
		return err
	}

	const chunk = 1000

	for c := 0; c < n; c += chunk {
		jobs := make([]*jobqueue.Job, 0, chunk)
		keys := make([]flat.Key, 0, chunk)

		for i := c; i < min(c+chunk, n); i++ {
			jobs = append(jobs, jobgen.Job(i, cmd))
			keys = append(keys, flat.KeyOf(jobgen.Key(jobs[len(jobs)-1])))
		}

		if err := s.Add(jobs, keys); err != nil {
			return err
		}

		for i := 0; i < len(jobs); i += 20 {
			st := flat.State{Key: keys[i], State: flat.SRunning, Pid: 77, Attempts: 1}
			st.SetHost("node-13-16")
			done, errf := s.Put(store.Start, &st, jobs[i])
			<-done

			if err := errf(); err != nil {
				return err
			}
		}
	}

	if err := s.Close(); err != nil {
		return err
	}

	fmt.Printf("built %s n=%d in %s\n", design, n, time.Since(t).Round(time.Millisecond))

	return nil
}

// evict drops the store's files from this host's page cache (for NFS, the
// client cache), so the next read is cold.
func evict(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}

		_ = unix.Fdatasync(int(f.Fd()))
		_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
		f.Close()
	}
}
