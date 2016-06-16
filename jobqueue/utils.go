// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

package jobqueue

// This file contains some general utility functions for use by client and
// server.

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"fmt"
	"github.com/dgryski/go-farm"
	"io"
	"os"
	"strconv"
)

var pss = []byte("Pss:")

// jobKey calculates a unique key to describe the job
func jobKey(job *Job) string {
	return byteKey([]byte(fmt.Sprintf("%s.%s", job.Cwd, job.Cmd)))
}

// byteKey calculates a unique key that describes a byte slice
func byteKey(b []byte) string {
	l, h := farm.Hash128(b)
	return fmt.Sprintf("%016x%016x", l, h)
}

// copy a file *** should be updated to handle source being on a different
// machine or in an S3-style object store
func copyFile(source string, dest string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	cerr := out.Close()
	if err != nil {
		return
	}
	if cerr != nil {
		err = cerr
		return
	}
	return
}

// compress uses zlib to compress stuff, for transferring big stuff like
// stdout, stderr and environment variables over the network, and for storing
// of same on disk
func compress(data []byte) []byte {
	var compressed bytes.Buffer
	w, _ := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	w.Write(data)
	w.Close()
	return compressed.Bytes()
}

// decompress uses zlib to decompress stuff compressed by compress()
func decompress(compressed []byte) (data []byte, err error) {
	b := bytes.NewReader(compressed)
	r, err := zlib.NewReader(b)
	if err != nil {
		return
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(r)
	data = buf.Bytes()
	return
}

// get the current memory usage of a pid, relying on modern linux /proc/*/smaps
// (based on http://stackoverflow.com/a/31881979/675083)
func currentMemory(pid int) (int, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/smaps", pid))
	if err != nil {
		return 0, err
	}
	defer f.Close()

	kb := uint64(0)
	r := bufio.NewScanner(f)
	for r.Scan() {
		line := r.Bytes()
		if bytes.HasPrefix(line, pss) {
			var size uint64
			_, err := fmt.Sscanf(string(line[4:]), "%d", &size)
			if err != nil {
				return 0, err
			}
			kb += size
		}
	}
	if err := r.Err(); err != nil {
		return 0, err
	}

	// convert kB to MB
	mem := int(kb / 1024)

	return mem, nil
}

// this prefixSuffixSaver-related code is taken from os/exec, since they are not
// exported. prefixSuffixSaver is an io.Writer which retains the first N bytes
// and the last N bytes written to it. The Bytes() methods reconstructs it with
// a pretty error message.
type prefixSuffixSaver struct {
	N         int
	prefix    []byte
	suffix    []byte
	suffixOff int
	skipped   int64
}

func (w *prefixSuffixSaver) Write(p []byte) (n int, err error) {
	lenp := len(p)
	p = w.fill(&w.prefix, p)
	if overage := len(p) - w.N; overage > 0 {
		p = p[overage:]
		w.skipped += int64(overage)
	}
	p = w.fill(&w.suffix, p)
	for len(p) > 0 { // 0, 1, or 2 iterations.
		n := copy(w.suffix[w.suffixOff:], p)
		p = p[n:]
		w.skipped += int64(n)
		w.suffixOff += n
		if w.suffixOff == w.N {
			w.suffixOff = 0
		}
	}
	return lenp, nil
}
func (w *prefixSuffixSaver) fill(dst *[]byte, p []byte) (pRemain []byte) {
	if remain := w.N - len(*dst); remain > 0 {
		add := minInt(len(p), remain)
		*dst = append(*dst, p[:add]...)
		p = p[add:]
	}
	return p
}
func (w *prefixSuffixSaver) Bytes() []byte {
	if w.suffix == nil {
		return w.prefix
	}
	if w.skipped == 0 {
		return append(w.prefix, w.suffix...)
	}
	var buf bytes.Buffer
	buf.Grow(len(w.prefix) + len(w.suffix) + 50)
	buf.Write(w.prefix)
	buf.WriteString("\n... omitting ")
	buf.WriteString(strconv.FormatInt(w.skipped, 10))
	buf.WriteString(" bytes ...\n")
	buf.Write(w.suffix[w.suffixOff:])
	buf.Write(w.suffix[:w.suffixOff])
	return buf.Bytes()
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
