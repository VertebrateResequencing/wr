// Copyright 2014 The Mangos Authors
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

// pubsub implements a publish/subscribe example.  server is a listening
// pub socket, and clients are dialing sub sockets.
//
// To use:
//
//   $ go build .
//   $ url=tcp://127.0.0.1:40899
//   $ ./pubsub server $url server & server=$! && sleep 1
//   $ ./pubsub client $url client0 & client0=$!
//   $ ./pubsub client $url client1 & client1=$!
//   $ ./pubsub client $url client2 & client2=$!
//   $ sleep 5
//   $ kill $server $client0 $client1 $client2
//
package main

import (
	"fmt"
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/pub"
	"github.com/go-mangos/mangos/protocol/sub"
	"github.com/go-mangos/mangos/transport/ipc"
	"github.com/go-mangos/mangos/transport/tcp"
	"os"
	"time"
)

func die(format string, v ...interface{}) {
	fmt.Fprintln(os.Stderr, fmt.Sprintf(format, v...))
	os.Exit(1)
}

func date() string {
	return time.Now().Format(time.ANSIC)
}

func server(url string) {
	var sock mangos.Socket
	var err error
	if sock, err = pub.NewSocket(); err != nil {
		die("can't get new pub socket: %s", err)
	}
	sock.AddTransport(ipc.NewTransport())
	sock.AddTransport(tcp.NewTransport())
	if err = sock.Listen(url); err != nil {
		die("can't listen on pub socket: %s", err.Error())
	}
	for {
		// Could also use sock.RecvMsg to get header
		d := date()
		fmt.Printf("SERVER: PUBLISHING DATE %s\n", d)
		if err = sock.Send([]byte(d)); err != nil {
			die("Failed publishing: %s", err.Error())
		}
		time.Sleep(time.Second)
	}
}

func client(url string, name string) {
	var sock mangos.Socket
	var err error
	var msg []byte

	if sock, err = sub.NewSocket(); err != nil {
		die("can't get new sub socket: %s", err.Error())
	}
	sock.AddTransport(ipc.NewTransport())
	sock.AddTransport(tcp.NewTransport())
	if err = sock.Dial(url); err != nil {
		die("can't dial on sub socket: %s", err.Error())
	}
	// Empty byte array effectively subscribes to everything
	err = sock.SetOption(mangos.OptionSubscribe, []byte(""))
	if err != nil {
		die("cannot subscribe: %s", err.Error())
	}
	for {
		if msg, err = sock.Recv(); err != nil {
			die("Cannot recv: %s", err.Error())
		}
		fmt.Printf("CLIENT(%s): RECEIVED %s\n", name, string(msg))
	}
}

func main() {
	if len(os.Args) > 2 && os.Args[1] == "server" {
		server(os.Args[2])
		os.Exit(0)
	}
	if len(os.Args) > 3 && os.Args[1] == "client" {
		client(os.Args[2], os.Args[3])
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "Usage: pubsub server|client <URL> <ARG>\n")
	os.Exit(1)
}
