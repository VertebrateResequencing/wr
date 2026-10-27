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
	"runtime"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/jobgen"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/store"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/zc"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"golang.org/x/sys/unix"
)

func main() {
	design := flag.String("design", "wal", "design")
	dir := flag.String("dir", "", "store directory")
	n := flag.Int("n", 120000, "live jobs")
	cmd := flag.Int("cmd", 10000, "command bytes")
	build := flag.Bool("build", true, "build the store first")
	codecName := flag.String("codec", "none", "spec compression for the log designs: none|s2|zstd|zstd-dict")
	batchName := flag.String("batchcodec", "none", "batch compression for the log designs: none|s2|zstd")
	dictFile := flag.String("dict", "", "zstd-dict's dictionary")
	corpus := flag.String("corpus", "", "build commands from this corpus of real commands")
	heap := flag.Bool("heap", false, "keep recovered jobs and report heap per live job")
	flag.Parse()

	c, err := zc.New(*codecName, *dictFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	store.SpecCodec = c

	if store.BatchCodec, err = zc.New(*batchName, *dictFile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if *corpus != "" {
		if jobgen.Corpus, err = zc.ReadCorpus(*corpus); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

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

			store.KeepJobs = *heap && temp == "warm"
			store.Kept = nil

			var before runtime.MemStats

			runtime.GC()
			runtime.ReadMemStats(&before)

			r, err := store.Recover(m, *dir, true)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}

			fmt.Printf("RECOVER design=%s fs=%s n=%d cmd=%d codec=%s/%s files=%d sizeMB=%.0f %s %s\n",
				m, filepath.Dir(*dir), *n, *cmd, *codecName, *batchName, len(ents), float64(size)/1e6, temp, r)

			if store.KeepJobs {
				var after runtime.MemStats

				runtime.GC()
				runtime.ReadMemStats(&after)
				fmt.Printf("HEAP kept=%d heapInuse=%.0fMB perJob=%.0fB\n", len(store.Kept),
					float64(after.HeapInuse-before.HeapInuse)/1e6,
					float64(after.HeapInuse-before.HeapInuse)/float64(max(len(store.Kept), 1)))
				runtime.KeepAlive(store.Kept)
			}
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
