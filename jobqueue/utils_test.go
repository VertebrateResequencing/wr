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

package jobqueue

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// memoryChildHoldMB is how much resident memory TestMemoryHoldingChild
	// holds, and memoryChildMinExcessMB the excess that currentMemory must
	// therefore report over ownMemoryMB. The slack between the two is what makes
	// the comparison sound: the two figures are separate /proc samples of a live
	// quantity, so they drift by whole MB, and only a signal far larger than
	// that drift makes the difference between them provable.
	memoryChildHoldMB      = 256
	memoryChildMinExcessMB = 128

	// memoryChildPageBytes is the granularity the child touches its allocation
	// at, so it becomes resident without writing every byte of it (which the
	// race detector would instrument 268 million times).
	memoryChildPageBytes = 4096

	// envMemoryChild makes TestMemoryHoldingChild hold memory instead of
	// skipping, and memoryChildReady is what it prints once that memory is
	// resident.
	envMemoryChild   = "WR_TEST_MEMORY_CHILD"
	memoryChildReady = "MEMHELD"
)

// TestMemoryHoldingChild is the child half of TestOwnMemoryMB: it makes
// memoryChildHoldMB of memory resident, says so, then holds it until its stdin
// closes.
func TestMemoryHoldingChild(t *testing.T) {
	if os.Getenv(envMemoryChild) == "" {
		t.Skip("child of TestOwnMemoryMB")
	}

	held := make([]byte, memoryChildHoldMB*bytesPerMB)
	for page := range len(held) / memoryChildPageBytes {
		held[page*memoryChildPageBytes] = 1
	}

	fmt.Println(memoryChildReady) //nolint:forbidigo

	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Logf("child stopped reading stdin: %s", err)
	}

	runtime.KeepAlive(held)
}

// TestOwnMemoryMB checks that ownMemoryMB reports this process's own Pss and,
// unlike currentMemory, excludes child processes.
//
// It proves that against a child holding a large known amount of memory, and
// deliberately returns pages to the OS between the two readings. An earlier
// version used no child and asserted ownMemoryMB() <= currentMemory(self)+1,
// which flaked ("Expected '480' to be less than or equal to '479'"): the two
// figures are separate samples of a live quantity, so Pss can drop between them
// by more than any fixed tolerance allows. That version also could not have
// failed had ownMemoryMB started counting children, which is the property it
// was there to check.
func TestOwnMemoryMB(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("ownMemoryMB reports this process's own memory without error", t, func() {
		mb, err := ownMemoryMB()
		So(err, ShouldBeNil)
		So(mb, ShouldBeGreaterThanOrEqualTo, 0)

		Convey("and it excludes children, which currentMemory includes", func() {
			stopChild := startMemoryHoldingChild(t)
			defer stopChild()

			own, err := ownMemoryMB()
			So(err, ShouldBeNil)

			// the flake this replaced came from the two figures being sampled at
			// different instants, so make that drift happen rather than hope it
			// does not: the assertions below have to survive it.
			debug.FreeOSMemory()

			withChildren, err := currentMemory(os.Getpid())
			So(err, ShouldBeNil)

			So(own, ShouldBeLessThan, withChildren)
			So(withChildren-own, ShouldBeGreaterThanOrEqualTo, memoryChildMinExcessMB)
		})
	})
}

// startMemoryHoldingChild runs this test binary as a child holding
// memoryChildHoldMB of resident memory, returning once that memory is resident.
// The returned function stops the child.
func startMemoryHoldingChild(t *testing.T) func() {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestMemoryHoldingChild$") //nolint:gosec

	cmd.Env = append(os.Environ(), envMemoryChild+"=1")

	stdin, err := cmd.StdinPipe()
	So(err, ShouldBeNil)

	stdout, err := cmd.StdoutPipe()
	So(err, ShouldBeNil)
	So(cmd.Start(), ShouldBeNil)

	stop := func() {
		_ = stdin.Close()

		if err := cmd.Wait(); err != nil {
			t.Logf("memory-holding child: %s", err)
		}
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == memoryChildReady {
			return stop
		}
	}

	stop()
	So("the child never reported holding memory", ShouldBeBlank)

	return func() {}
}

func TestLiveTailSaver(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A live tail saver flushes a compressed recent tail", t, func() {
		saver := &liveTailSaver{}

		n, err := saver.Write([]byte("one\n"))
		So(err, ShouldBeNil)
		So(n, ShouldEqual, len("one\n"))

		compressed := saver.FlushCompressed()
		So(compressed, ShouldNotBeNil)
		So(len(compressed), ShouldBeLessThanOrEqualTo, liveStdCompressedLimit)
		So(decompressLiveTail(compressed), ShouldResemble, []byte("one\n"))
	})

	Convey("A live tail saver returns nil when flushed twice without more writes", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("one\n"))
		So(err, ShouldBeNil)
		So(saver.FlushCompressed(), ShouldNotBeNil)
		So(saver.FlushCompressed(), ShouldBeNil)
	})

	Convey("A live tail saver bounds incompressible output to a compressed suffix", t, func() {
		written := deterministicLiveBytes(liveStdRawTailLimit)
		saver := &liveTailSaver{}

		n, err := saver.Write(written)
		So(err, ShouldBeNil)
		So(n, ShouldEqual, len(written))

		compressed := saver.FlushCompressed()
		So(compressed, ShouldNotBeNil)
		So(len(compressed), ShouldBeLessThanOrEqualTo, liveStdCompressedLimit)

		decompressed := decompressLiveTail(compressed)
		So(decompressed, ShouldNotBeEmpty)
		So(bytes.HasSuffix(written, decompressed), ShouldBeTrue)
	})

	Convey("A live tail saver keeps the newest marker and drops old output", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("UNIQUE-PREFIX\n"))
		So(err, ShouldBeNil)
		_, err = saver.Write(deterministicLiveBytes(2 * liveStdRawTailLimit))
		So(err, ShouldBeNil)
		_, err = saver.Write([]byte("UNIQUE-SUFFIX\n"))
		So(err, ShouldBeNil)

		decompressed := decompressLiveTail(saver.FlushCompressed())
		So(string(decompressed), ShouldContainSubstring, "UNIQUE-SUFFIX\n")
		So(string(decompressed), ShouldNotContainSubstring, "UNIQUE-PREFIX\n")
	})

	Convey("A live tail saver resets after each flush", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("old\n"))
		So(err, ShouldBeNil)
		So(saver.FlushCompressed(), ShouldNotBeNil)

		_, err = saver.Write([]byte("new\n"))
		So(err, ShouldBeNil)
		So(decompressLiveTail(saver.FlushCompressed()), ShouldResemble, []byte("new\n"))
	})

	Convey("A live tail saver lets writes continue while flushing compressed output", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("old\n"))
		So(err, ShouldBeNil)

		started := make(chan struct{})
		release := make(chan struct{})
		originalCompressor := liveTailCompressor

		liveTailCompressor = func(tail []byte) []byte {
			close(started)
			<-release

			return originalCompressor(tail)
		}
		defer func() {
			liveTailCompressor = originalCompressor
		}()

		flushed := make(chan []byte, 1)
		go func() {
			flushed <- saver.FlushCompressed()
		}()

		<-started

		writeDone := make(chan error, 1)

		go func() {
			_, writeErr := saver.Write([]byte("new\n"))
			writeDone <- writeErr
		}()

		writeCompleted := false

		select {
		case writeErr := <-writeDone:
			So(writeErr, ShouldBeNil)

			writeCompleted = true
		case <-time.After(200 * time.Millisecond):
		}

		close(release)
		So(writeCompleted, ShouldBeTrue)

		flushedCompressed := <-flushed
		liveTailCompressor = originalCompressor

		So(decompressLiveTail(flushedCompressed), ShouldResemble, []byte("old\n"))
		So(decompressLiveTail(saver.FlushCompressed()), ShouldResemble, []byte("new\n"))
	})
}

func decompressLiveTail(compressed []byte) []byte {
	decompressed, err := decompress(compressed)
	So(err, ShouldBeNil)

	return decompressed
}

//nolint:gosec // deterministic test data must be reproducible.
func deterministicLiveBytes(size int) []byte {
	r := rand.New(rand.NewSource(1))

	data := make([]byte, size)
	for i := range data {
		data[i] = byte(r.Intn(256))
	}

	return data
}
