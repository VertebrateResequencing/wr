// Command nfsprobe samples, for hours, how long a small durable append takes
// in one directory, to show how often and for how long a filesystem stalls a
// log's group commit. Every -every it appends -size bytes to a log file and
// fdatasyncs it, and every -bigevery it does the same with -bigsize bytes (an
// add's batch). Each sample is written as "unixnano kind micros" to -out; a
// per-minute summary goes to stdout. The log file is truncated once it passes
// 256MB so the probe never fills the filesystem.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/stats"
	"golang.org/x/sys/unix"
)

const maxLog = 256 << 20

func main() {
	dir := flag.String("dir", "", "directory to probe")
	out := flag.String("out", "", "file to write every sample to")
	every := flag.Duration("every", 100*time.Millisecond, "small append interval")
	size := flag.Int("size", 4096, "small append bytes")
	bigEvery := flag.Duration("bigevery", 10*time.Second, "big append interval")
	bigSize := flag.Int("bigsize", 1<<20, "big append bytes")
	dur := flag.Duration("dur", 10*time.Hour, "how long to probe")
	flag.Parse()

	path := filepath.Join(*dir, "nfsprobe.log")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		panic(err)
	}
	defer os.Remove(path)
	defer f.Close()

	of, err := os.Create(*out)
	if err != nil {
		panic(err)
	}
	defer of.Close()

	w := bufio.NewWriter(of)
	defer w.Flush()

	small := make([]byte, *size)
	big := make([]byte, *bigSize)

	var written int64

	write := func(buf []byte, kind string, lat *stats.Lat) {
		if written > maxLog {
			if err := f.Truncate(0); err != nil {
				panic(err)
			}

			if _, err := f.Seek(0, 0); err != nil {
				panic(err)
			}

			written = 0
		}

		t := time.Now()

		if _, err := f.Write(buf); err != nil {
			fmt.Fprintf(w, "%d %s-err %v\n", t.UnixNano(), kind, err)

			return
		}

		if err := unix.Fdatasync(int(f.Fd())); err != nil {
			fmt.Fprintf(w, "%d %s-err %v\n", t.UnixNano(), kind, err)

			return
		}

		d := time.Since(t)
		written += int64(len(buf))
		lat.Add(d)
		fmt.Fprintf(w, "%d %s %d\n", t.UnixNano(), kind, d.Microseconds())
	}

	end := time.Now().Add(*dur)
	nextBig := time.Now()
	minute := time.Now().Truncate(time.Minute).Add(time.Minute)
	sl, bl := stats.New(), stats.New()

	for time.Now().Before(end) {
		t := time.Now()

		write(small, "s", sl)

		if !t.Before(nextBig) {
			write(big, "b", bl)
			nextBig = t.Add(*bigEvery)
		}

		if !t.Before(minute) {
			fmt.Printf("%s small %s | big %s\n", minute.Format("15:04"), sl, bl)
			w.Flush()

			sl, bl = stats.New(), stats.New()
			minute = minute.Add(time.Minute)
		}

		if s := *every - time.Since(t); s > 0 {
			time.Sleep(s)
		}
	}
}
