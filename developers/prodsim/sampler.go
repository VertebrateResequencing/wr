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

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
)

const (
	sampleHeader = "t_s\tpid\trss_mb\thwm_mb\tthreads\tfds\tgoroutines\theap_inuse_mb\theap_objects\t" +
		"db_mb\tdbbk_mb\tlog_mb\tping_ms\tsubmitted\tload1"

	pingTimeout      = time.Minute
	sampleConnect    = 30 * time.Second
	pprofTimeout     = 90 * time.Second
	firstProfileFrac = 3
	kib              = 1024
	mibShift         = 20
	unknown          = -1
)

// sampler appends the manager's health to samples.tsv every -sample-every,
// and saves pprof profiles every -profile-every, until the run ends.
func (s *sim) sampler(ctx context.Context) {
	pp := &pprofClient{addr: s.cfg.pprofAddr, http: &http.Client{Timeout: pprofTimeout}}
	jq := s.newJQ(ctx, "sampler")
	nextProfile := time.Now().Add(s.cfg.profileEvery / firstProfileFrac)

	for {
		var pingMS int64

		jq, pingMS = s.ping(ctx, jq)
		s.sample(ctx, pp, pingMS)

		if pp.addr != "" && time.Now().After(nextProfile) {
			s.captureProfiles(ctx, pp)
			nextProfile = time.Now().Add(s.cfg.profileEvery)
		}

		if !sleep(ctx, s.cfg.sampleEvery) {
			break
		}
	}

	if pp.addr != "" {
		// ctx has ended, but the manager is still up for a last look
		s.captureProfiles(context.WithoutCancel(ctx), pp)
	}

	if jq != nil {
		disconnect(jq)
	}
}

// ping times a ping over jq, returning -1 when it fails. A failed client is
// dropped and a new one tried at the next sample, so a restart is survived.
func (s *sim) ping(ctx context.Context, jq *jobqueue.Client) (*jobqueue.Client, int64) {
	if jq == nil {
		fresh, err := jobqueue.ConnectUsingConfig(ctx, s.cfg.deployment, sampleConnect)
		if err != nil {
			return nil, unknown
		}

		return fresh, unknown
	}

	t0 := time.Now()

	if _, err := jq.Ping(pingTimeout); err != nil {
		disconnect(jq)

		return nil, unknown
	}

	return jq, time.Since(t0).Milliseconds()
}

// sample appends one row to samples.tsv.
func (s *sim) sample(ctx context.Context, pp *pprofClient, pingMS int64) {
	pid := readTrim(filepath.Join(s.cfg.runDir, "pid"))
	st := procStatus(pid)
	gor, heap, objs := pp.summary(ctx)
	load, _, _ := strings.Cut(readTrim("/proc/loadavg"), " ")

	s.samples.line(fmt.Sprintf("%.1f\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s", s.since(), pid,
		st.rssMB, st.hwmMB, st.threads, countDir(fmt.Sprintf("/proc/%s/fd", pid)), gor, heap, objs,
		fileMB(filepath.Join(s.cfg.runDir, "db")), fileMB(filepath.Join(s.cfg.runDir, "db_bk")),
		fileMB(filepath.Join(s.cfg.runDir, "log")), pingMS, s.submitted.Load(), load))
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(b))
}

// procStat is what the sampler reads from /proc/<pid>/status.
type procStat struct {
	rssMB, hwmMB, threads int
}

// procStatus reads the manager's RSS, peak RSS and thread count, which are all
// -1 if it cannot.
func procStatus(pid string) procStat {
	st := procStat{unknown, unknown, unknown}
	if pid == "" {
		return st
	}

	b, err := os.ReadFile("/proc/" + pid + "/status")
	if err != nil {
		return st
	}

	fields := map[string]*int{"VmRSS:": &st.rssMB, "VmHWM:": &st.hwmMB, "Threads:": &st.threads}
	divisors := map[string]int{"VmRSS:": kib, "VmHWM:": kib, "Threads:": 1}

	for line := range strings.SplitSeq(string(b), "\n") {
		name, value, _ := strings.Cut(line, "\t")
		if dst, ok := fields[name]; ok {
			*dst = atoiOr(strings.Fields(value), unknown) / divisors[name]
		}
	}

	return st
}

// atoiOr returns the first field as an int, or def.
func atoiOr(fields []string, def int) int {
	if len(fields) == 0 {
		return def
	}

	v, err := strconv.Atoi(fields[0])
	if err != nil {
		return def
	}

	return v
}

func countDir(path string) int {
	entries, err := os.ReadDir(path)
	if err != nil {
		return unknown
	}

	return len(entries)
}

func fileMB(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return unknown
	}

	return fi.Size() >> mibShift
}

// pprofClient reads the manager's WR_PPROF_ADDR endpoint.
type pprofClient struct {
	addr string
	http *http.Client
}

func (p *pprofClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+p.addr+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() {
		if errc := resp.Body.Close(); errc != nil {
			fmt.Fprintln(os.Stderr, "prodsim: pprof:", errc)
		}
	}()

	return io.ReadAll(resp.Body)
}

// summary returns the goroutine count, and the heap in use (MB) and object
// count, or -1s.
func (p *pprofClient) summary(ctx context.Context) (goroutines, heapMB, objects int) {
	if p.addr == "" {
		return unknown, unknown, unknown
	}

	return p.goroutines(ctx), p.heapStat(ctx, "# HeapInuse = ") >> mibShift, p.heapStat(ctx, "# HeapObjects = ")
}

// goroutines returns the "total N" of the goroutine profile's first line.
func (p *pprofClient) goroutines(ctx context.Context) int {
	b, err := p.get(ctx, "/debug/pprof/goroutine?debug=1")
	if err != nil {
		return unknown
	}

	line, _, _ := strings.Cut(string(b), "\n")

	_, total, found := strings.Cut(line, "total ")
	if !found {
		return unknown
	}

	return atoiOr(strings.Fields(total), unknown)
}

// heapStat returns the value of the heap profile's line starting with prefix.
func (p *pprofClient) heapStat(ctx context.Context, prefix string) int {
	b, err := p.get(ctx, "/debug/pprof/heap?debug=1")
	if err != nil {
		return unknown
	}

	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			return atoiOr(strings.Fields(v), unknown)
		}
	}

	return unknown
}

// captureProfiles saves the manager's heap, goroutine, mutex, block and 30s
// CPU profiles, and a readable goroutine dump, under profiles/.
func (s *sim) captureProfiles(ctx context.Context, pp *pprofClient) {
	stamp := fmt.Sprintf("%06.0f", s.since())
	dir := filepath.Join(s.cfg.outDir, "profiles")

	for _, prof := range []struct{ file, path string }{
		{"heap.pprof", "/debug/pprof/heap"},
		{"goroutine.pprof", "/debug/pprof/goroutine"},
		{"mutex.pprof", "/debug/pprof/mutex"},
		{"block.pprof", "/debug/pprof/block"},
		{"cpu.pprof", "/debug/pprof/profile?seconds=30"},
		{"goroutine.txt", "/debug/pprof/goroutine?debug=1"},
	} {
		b, err := pp.get(ctx, prof.path)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, stamp+"."+prof.file), b, filePerm)
		}

		if err != nil {
			s.event("sampler", "profile "+prof.file+" failed: "+err.Error())
		}
	}
}
