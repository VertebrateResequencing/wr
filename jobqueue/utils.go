// Copyright © 2016-2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

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
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// AppName gets used in certain places like naming the base directory of created
// working directories during Client.Execute().
var AppName = "jobqueue"

// mkHashedLevels is the number of directory levels we create in mkHashedDirs
const mkHashedLevels = 4

var pss = []byte("Pss:")

// cr, lf and ellipses get used by stdFilter()
var cr = []byte("\r")
var lf = []byte("\n")
var ellipses = []byte("[...]\n")

// CurrentIP returns the IP address of the machine we're running on right now.
// The cidr argument can be an empty string, but if set to the CIDR of the
// machine's primary network, it helps us be sure of getting the correct IP
// address (for when there are multiple network interfaces on the machine).
func CurrentIP(cidr string) (ip string) {
	var ipNet *net.IPNet
	if cidr != "" {
		_, ipn, err := net.ParseCIDR(cidr)
		if err == nil {
			ipNet = ipn
		}
		// *** ignoring error since I don't want to change the return value of
		// this method...
	}

	// first just hope http://stackoverflow.com/a/25851186/675083 gives us a
	// cross-linux&MacOS solution that works reliably...
	out, err := exec.Command("sh", "-c", "ip -4 route get 8.8.8.8 | head -1 | cut -d' ' -f8 | tr -d '\\n'").Output()
	if err == nil {
		ip = string(out)

		// paranoid confirmation this ip is in our CIDR
		if ip != "" && ipNet != nil {
			pip := net.ParseIP(ip)
			if pip != nil {
				if !ipNet.Contains(pip) {
					ip = ""
				}
			}
		}
	}

	// if the above fails, fall back on manually going through all our network
	// interfaces
	if ip == "" {
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return
		}
		for _, address := range addrs {
			if thisIPNet, ok := address.(*net.IPNet); ok && !thisIPNet.IP.IsLoopback() {
				if thisIPNet.IP.To4() != nil {
					if ipNet != nil {
						if ipNet.Contains(thisIPNet.IP) {
							ip = thisIPNet.IP.String()
							break
						}
					} else {
						ip = thisIPNet.IP.String()
						break
					}
				}
			}
		}
	}

	return
}

// byteKey calculates a unique key that describes a byte slice.
func byteKey(b []byte) string {
	l, h := farm.Hash128(b)
	return fmt.Sprintf("%016x%016x", l, h)
}

// copy a file *** should be updated to handle source being on a different
// machine or in an S3-style object store.
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
// of same on disk.
func compress(data []byte) []byte {
	var compressed bytes.Buffer
	w, _ := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	w.Write(data)
	w.Close()
	return compressed.Bytes()
}

// decompress uses zlib to decompress stuff compressed by compress().
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
// (based on http://stackoverflow.com/a/31881979/675083).
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

// stdFilter keeps only the first and last line of any contiguous block of \r
// terminated lines (to mostly eliminate progress bars), intended for use with
// stdout/err streaming input, outputting to a prefixSuffixSaver. Because you
// must finish reading from the input before continuing, it returns a channel
// that you should wait to receive something from.
func stdFilter(std io.Reader, out io.Writer) chan bool {
	reader := bufio.NewReader(std)
	done := make(chan bool)
	go func() {
		for {
			p, err := reader.ReadBytes('\n')

			lines := bytes.Split(p, cr)
			out.Write(lines[0])
			if len(lines) > 2 {
				out.Write(lf)
				if len(lines) > 3 {
					out.Write(ellipses)
				}
				out.Write(lines[len(lines)-2])
				out.Write(lf)
			}

			if err != nil {
				break
			}
		}
		done <- true
	}()
	return done
}

// envOverride deals with values you get from os.Environ, overriding one set
// with values from another.
func envOverride(orig []string, over []string) (env []string) {
	override := make(map[string]string)
	for _, envvar := range over {
		pair := strings.Split(envvar, "=")
		override[pair[0]] = envvar
	}

	env = orig
	for i, envvar := range env {
		pair := strings.Split(envvar, "=")
		if replace, do := override[pair[0]]; do {
			env[i] = replace
			delete(override, pair[0])
		}
	}

	for _, envvar := range override {
		env = append(env, envvar)
	}
	return
}

// mkHashedDir uses tohash (which should be a 32 char long string from
// byteKey()) to create a folder nested within baseDir, and in that folder
// creates 2 folders called cwd and tmp, which it returns. Returns an error if
// there were problems making the directories.
func mkHashedDir(baseDir, tohash string) (cwd, tmpDir string, err error) {
	dirs := strings.SplitN(tohash, "", mkHashedLevels)
	dirs, leaf := dirs[0:mkHashedLevels-1], dirs[mkHashedLevels-1]
	dirs = append([]string{baseDir, AppName + "_cwd"}, dirs...)
	dir := filepath.Join(dirs...)
	err = os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return
	}

	// if tohash is a job key then we expect that only 1 of that job is
	// running at any one time per jobqueue, but there could be multiple users
	// running the same cmd, or this user could be running the same command in
	// multiple queues, so we must still create a unique dir at the leaf of our
	// hashed dir structure, to avoid any conflict of multiple processes using
	// the same working directory
	dir, err = ioutil.TempDir(dir, leaf)
	if err != nil {
		return
	}

	cwd = filepath.Join(dir, "cwd")
	err = os.Mkdir(cwd, os.ModePerm)
	if err != nil {
		return
	}

	tmpDir = filepath.Join(dir, "tmp")
	err = os.Mkdir(tmpDir, os.ModePerm)
	return
}

// rmEmptyDirs deletes leafDir and it's parent directories if they are empty,
// stopping if it reaches baseDir (leaving that undeleted). It's ok if leafDir
// doesn't exist.
func rmEmptyDirs(leafDir, baseDir string) {
	os.Remove(leafDir)
	current := leafDir
	parent := filepath.Dir(current)
	for ; parent != baseDir; parent = filepath.Dir(current) {
		thisErr := os.Remove(parent)
		if thisErr != nil {
			// it's expected that we might not be able to delete parents, since
			// some other Job may be running from the same Cwd, meaning this
			// parent dir is not empty
			break
		}
		current = parent
	}
}

// removeAllExcept deletes the contents of a given directory (absolute path),
// except for the given folders (relative paths).
func removeAllExcept(path string, exceptions []string) error {
	keepDirs := make(map[string]bool)
	checkDirs := make(map[string]bool)
	path = filepath.Clean(path)
	for _, dir := range exceptions {
		abs := filepath.Join(path, dir)
		keepDirs[abs] = true
		parent := filepath.Dir(abs)
		for {
			if parent == path {
				break
			}
			checkDirs[parent] = true
			parent = filepath.Dir(parent)
		}
	}

	return removeWithExceptions(path, keepDirs, checkDirs)
}

// removeWithExceptions is the recursive part of removeAllExcept's
// implementation that does the real work of deleting stuff.
func removeWithExceptions(path string, keepDirs map[string]bool, checkDirs map[string]bool) error {
	entries, err := ioutil.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		abs := filepath.Join(path, entry.Name())
		if !entry.IsDir() {
			err := os.Remove(abs)
			if err != nil {
				return err
			}
			continue
		}

		if keepDirs[abs] {
			continue
		}

		if checkDirs[abs] {
			err = removeWithExceptions(abs, keepDirs, checkDirs)
			if err != nil {
				return err
			}
		} else {
			err = os.RemoveAll(abs)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
