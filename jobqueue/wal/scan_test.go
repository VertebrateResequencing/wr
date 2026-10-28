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

package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// errStop is the error TestScan's fn stops a scan with.
var errStop = errors.New("stop")

// scanned is what scanAll saw.
type scanned struct {
	refs []Ref
	recs []string
	end  int64
	err  error
}

// scanAll scans log, keeping a copy of each record.
func scanAll(log []byte) scanned {
	var s scanned

	s.end, s.err = Scan(bytes.NewReader(log), func(ref Ref, rec []byte) error {
		s.refs = append(s.refs, ref)
		s.recs = append(s.recs, string(rec))

		return nil
	})

	return s
}

func TestScan(t *testing.T) {
	Convey("Given frames for records a, bb and ccc", t, func() {
		log := appendFrames("a", "bb", "ccc")

		Convey("a scan sees all 3 in order at their offsets, and ends after the last", func() {
			s := scanAll(log)
			So(s.err, ShouldBeNil)
			So(s.recs, ShouldResemble, []string{"a", "bb", "ccc"})
			So(s.refs, ShouldResemble, []Ref{{0, 0, 0}, {0, 9, 0}, {0, 19, 0}})
			So(s.end, ShouldEqual, 30)
		})

		Convey("a scan of the log cut at every offset sees the whole frames before the cut", func() {
			wholeBefore := func(cut int) int {
				switch {
				case cut >= 30:
					return 3
				case cut >= 19:
					return 2
				case cut >= 9:
					return 1
				default:
					return 0
				}
			}

			wrong := 0

			for cut := range len(log) {
				s := scanAll(log[:cut])
				if s.err != nil || len(s.recs) != wholeBefore(cut) {
					wrong++
				}
			}

			So(wrong, ShouldEqual, 0)
		})

		Convey("a flipped payload byte ends the scan before that frame, with no error", func() {
			log[FrameHeader+9+1] ^= 0xff

			s := scanAll(log)
			So(s.err, ShouldBeNil)
			So(s.recs, ShouldResemble, []string{"a"})
			So(s.end, ShouldEqual, 9)
		})

		Convey("an error from fn stops the scan and is returned, with end before that frame", func() {
			seen := 0

			end, err := Scan(bytes.NewReader(log), func(Ref, []byte) error {
				seen++
				if seen == 2 {
					return errStop
				}

				return nil
			})
			So(err, ShouldEqual, errStop)
			So(seen, ShouldEqual, 2)
			So(end, ShouldEqual, 9)
		})
	})

	Convey("Given a frame whose length has bit 31 clear and value 2^30 + 1, a scan stops there with no error", t, func() {
		log := appendFrames("a")
		log = binary.LittleEndian.AppendUint32(log, 1<<30+1)
		log = binary.LittleEndian.AppendUint32(log, 0)
		log = append(log, make([]byte, 64)...)

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, []string{"a"})
		So(s.end, ShouldEqual, 9)
	})
}

// appendFrames frames each record onto one log.
func appendFrames(recs ...string) []byte {
	var log []byte

	for _, rec := range recs {
		log = AppendFrame(log, []byte(rec))
	}

	return log
}

func TestScanDamagedLength(t *testing.T) {
	Convey("Given a frame claiming a 1GiB payload followed by only 64 bytes, a scan stops there with no "+
		"error, allocating far less than the claimed length", t, func() {
		log := appendFrames("a")
		log = binary.LittleEndian.AppendUint32(log, MaxFrameLen)
		log = binary.LittleEndian.AppendUint32(log, 0)
		log = append(log, make([]byte, 64)...)

		var before, after runtime.MemStats

		runtime.GC()
		runtime.ReadMemStats(&before)

		s := scanAll(log)

		runtime.ReadMemStats(&after)

		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, []string{"a"})
		So(s.end, ShouldEqual, 9)
		So(after.TotalAlloc-before.TotalAlloc, ShouldBeLessThan, 8<<20)
	})
}
