// Command fsyncbench measures the storage primitives every durable design is
// built from, in one directory: append+fdatasync latency by write size, the
// same from several files at once, and create+fsync+rename (the per-key-file
// commit).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/stats"
	"golang.org/x/sys/unix"
)

func main() {
	dir := flag.String("dir", "", "directory to test in")
	n := flag.Int("n", 200, "iterations per case")
	flag.Parse()

	for _, size := range []int{128, 4096, 65536, 1 << 20, 8 << 20} {
		appendSync(*dir, size, *n, 1)
	}

	for _, par := range []int{4, 16, 64} {
		appendSync(*dir, 4096, *n, par)
	}

	renameCommit(*dir, 4096, *n, 1)
	renameCommit(*dir, 4096, *n, 16)
	renameCommit(*dir, 4096, *n, 64)
}

func appendSync(dir string, size, n, par int) {
	buf := make([]byte, size)
	lat := stats.New()

	var wg sync.WaitGroup

	start := time.Now()

	for p := range par {
		wg.Add(1)

		go func() {
			defer wg.Done()

			path := filepath.Join(dir, fmt.Sprintf("append.%d", p))

			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_TRUNC, 0o600)
			if err != nil {
				panic(err)
			}
			defer os.Remove(path)
			defer f.Close()

			for range max(n/par, 20) {
				t := time.Now()

				if _, err := f.Write(buf); err != nil {
					panic(err)
				}

				if err := unix.Fdatasync(int(f.Fd())); err != nil {
					panic(err)
				}

				lat.Add(time.Since(t))
			}
		}()
	}

	wg.Wait()

	el := time.Since(start)
	fmt.Printf("append+fdatasync size=%-8d par=%-3d ops=%-5d %s ops/s=%.0f MB/s=%.1f\n",
		size, par, lat.N(), lat, float64(lat.N())/el.Seconds(), float64(lat.N()*size)/el.Seconds()/1e6)
}

func renameCommit(dir string, size, n, par int) {
	buf := make([]byte, size)
	lat := stats.New()

	var wg sync.WaitGroup

	start := time.Now()

	for p := range par {
		wg.Add(1)

		go func() {
			defer wg.Done()

			sub := filepath.Join(dir, fmt.Sprintf("rc%d", p))
			if err := os.MkdirAll(sub, 0o700); err != nil {
				panic(err)
			}
			defer os.RemoveAll(sub)

			d, err := os.Open(sub)
			if err != nil {
				panic(err)
			}
			defer d.Close()

			for i := range max(n/par, 20) {
				t := time.Now()
				tmp := filepath.Join(sub, fmt.Sprintf(".tmp%d", i))

				f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
				if err != nil {
					panic(err)
				}

				if _, err = f.Write(buf); err != nil {
					panic(err)
				}

				if err = unix.Fdatasync(int(f.Fd())); err != nil {
					panic(err)
				}

				f.Close()

				if err = os.Rename(tmp, filepath.Join(sub, fmt.Sprintf("k%d", i))); err != nil {
					panic(err)
				}

				if err = unix.Fsync(int(d.Fd())); err != nil {
					panic(err)
				}

				lat.Add(time.Since(t))
			}
		}()
	}

	wg.Wait()

	el := time.Since(start)
	fmt.Printf("create+fdatasync+rename+dirsync size=%d par=%-3d ops=%-5d %s ops/s=%.0f\n",
		size, par, lat.N(), lat, float64(lat.N())/el.Seconds())
}
