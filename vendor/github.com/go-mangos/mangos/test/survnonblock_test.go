// Copyright 2016 The Mangos Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use file except in compliance with the License.
// You may obtain a copy of the license at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package test

import (
	"testing"
	"time"

	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/surveyor"
	"github.com/go-mangos/mangos/transport/tcp"

	. "github.com/smartystreets/goconvey/convey"
)

func testSurvNonBlock(addr string, tran mangos.Transport) {
	maxqlen := 2
	timeout := time.Second / 10

	Convey("Given a suitable Surveyor socket", func() {
		rp, err := surveyor.NewSocket()
		So(err, ShouldBeNil)
		So(rp, ShouldNotBeNil)
		defer rp.Close()

		rp.AddTransport(tran)

		err = rp.SetOption(mangos.OptionWriteQLen, maxqlen)
		So(err, ShouldBeNil)

		err = rp.SetOption(mangos.OptionSendDeadline, timeout)
		So(err, ShouldBeNil)

		err = rp.Listen(addr)
		So(err, ShouldBeNil)

		msg := []byte{'A', 'B', 'C'}

		Convey("We don't block, even sending many messages", func() {
			for i := 0; i < maxqlen*10; i++ {

				err := rp.Send(msg)
				So(err, ShouldBeNil)
			}
		})
	})
}

func TestSurveyorNonBlockTCP(t *testing.T) {
	Convey("Testing Survey Send (TCP) is Non-Blocking", t, func() {
		testSurvNonBlock(AddrTestTCP, tcp.NewTransport())
	})
}
