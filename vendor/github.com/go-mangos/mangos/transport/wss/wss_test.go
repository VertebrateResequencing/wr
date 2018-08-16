// Copyright 2015 The Mangos Authors
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

package wss

import (
	"testing"

	"github.com/go-mangos/mangos/test"
)

var tt = test.NewTranTest(NewTransport(), "wss://127.0.0.1:3346/mysock")

func TestWSSListenAndAccept(t *testing.T) {
	tt.TestListenAndAccept(t)
}

func TestWSSDuplicateListen(t *testing.T) {
	tt.TestDuplicateListen(t)
}

func TestWSSConnRefused(t *testing.T) {
	tt.TestConnRefused(t)
}

func TestWSSSendRecv(t *testing.T) {
	tt.TestSendRecv(t)
}
