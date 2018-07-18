// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of l15h.
//
//  l15h is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  l15h is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with l15h. If not, see <http://www.gnu.org/licenses/>.

package l15h_test

import (
	"bytes"
	log "github.com/inconshreveable/log15"
	"github.com/sb10/l15h"
	. "github.com/smartystreets/goconvey/convey"
	"os"
	"os/exec"
	"testing"
)

func TestStore(t *testing.T) {
	Convey("You can set up the StoreHandler", t, func() {
		buff := new(bytes.Buffer)

		store := l15h.NewStore()
		h := log.MultiHandler(
			l15h.StoreHandler(store, log.LogfmtFormat()),
			log.StreamHandler(buff, log.LogfmtFormat()),
		)
		log.Root().SetHandler(h)

		Convey("You can log multiple messages", func() {
			log.Info("one")
			one := buff.String()
			So(one, ShouldContainSubstring, "msg=one")
			buff.Reset()
			log.Info("two")
			two := buff.String()
			So(two, ShouldContainSubstring, "msg=two")
			buff.Reset()
			log.Info("three")
			three := buff.String()
			So(three, ShouldContainSubstring, "msg=three")
			buff.Reset()

			Convey("You can get all messages with Log()", func() {
				So(store.Logs(), ShouldResemble, []string{one, two, three})

				Convey("You can Clear()", func() {
					store.Clear()
					So(store.Logs(), ShouldBeEmpty)

					Convey("And add more logs", func() {
						log.Info("four")
						four := buff.String()
						So(four, ShouldContainSubstring, "msg=four")
						buff.Reset()
						So(store.Logs(), ShouldResemble, []string{four})
					})
				})
			})
		})

		Reset(func() {
			buff.Reset()
		})
	})
}

func TestCaller(t *testing.T) {
	Convey("You can set up the CallerInfoHandler", t, func() {
		buff := new(bytes.Buffer)
		msg := "msg"
		em := " msg=msg"
		ec := " caller=l15h_test.go:"

		h := l15h.CallerInfoHandler(log.StreamHandler(buff, log.LogfmtFormat()))
		log.Root().SetHandler(h)

		Convey("Debug() includes caller", func() {
			log.Debug(msg)
			lm := buff.String()
			So(lm, ShouldContainSubstring, em)
			So(lm, ShouldContainSubstring, ec)
		})

		Convey("Info() doesn't include caller", func() {
			log.Info(msg)
			lm := buff.String()
			So(lm, ShouldContainSubstring, em)
			So(lm, ShouldNotContainSubstring, ec)
		})

		Convey("Warn() includes caller", func() {
			log.Warn(msg)
			lm := buff.String()
			So(lm, ShouldContainSubstring, em)
			So(lm, ShouldContainSubstring, ec)
		})

		Convey("Error() includes caller", func() {
			log.Error(msg)
			lm := buff.String()
			So(lm, ShouldContainSubstring, em)
			So(lm, ShouldContainSubstring, ec)
		})

		Convey("Crit() includes stack", func() {
			log.Crit(msg)
			lm := buff.String()
			So(lm, ShouldContainSubstring, em)
			So(lm, ShouldContainSubstring, ` stack="[github.com/sb10/l15h/l15h_test.go:`)
		})

		Reset(func() {
			buff.Reset()
		})
	})
}

func TestChanger(t *testing.T) {
	Convey("You can set up the ChangeableHandler", t, func() {
		buff := new(bytes.Buffer)

		changer := l15h.NewChanger(log.DiscardHandler())
		log.Root().SetHandler(l15h.ChangeableHandler(changer))

		Convey("You can log a message to the original handler", func() {
			log.Info("one")
			So(buff.String(), ShouldBeEmpty)

			Convey("You can create a new logger that inherits the changer and adds a new Handler", func() {
				child := log.New("child", "true")
				store := l15h.NewStore()
				l15h.AddHandler(child, l15h.StoreHandler(store, log.LogfmtFormat()))

				log.Info("two")
				So(buff.String(), ShouldBeEmpty)
				child.Info("1")
				So(buff.String(), ShouldBeEmpty)
				So(len(store.Logs()), ShouldEqual, 1)

				Convey("You can change the logger and it affects the root and child logger", func() {
					changer.SetHandler(log.StreamHandler(buff, log.LogfmtFormat()))

					log.Info("three")
					So(buff.String(), ShouldContainSubstring, " msg=three")
					So(buff.String(), ShouldNotContainSubstring, "child")
					buff.Reset()
					child.Info("2")
					So(buff.String(), ShouldContainSubstring, " msg=2")
					So(buff.String(), ShouldContainSubstring, " child=true")
					So(len(store.Logs()), ShouldEqual, 2)
				})
			})
		})
	})
}

func TestPanic(t *testing.T) {
	Convey("You can Panic() at the root level", t, func() {
		buff := new(bytes.Buffer)
		msg := "msg"

		h := log.StreamHandler(buff, log.LogfmtFormat())
		log.Root().SetHandler(h)

		So(func() { l15h.Panic(msg) }, ShouldPanic)
		So(buff.String(), ShouldContainSubstring, " lvl=crit msg=msg panic=true")
		buff.Reset()

		Convey("And on a logger with context", func() {
			logger := log.New("child", "context")
			So(func() { l15h.PanicContext(logger, msg, "extra", "stuff") }, ShouldPanic)
			logger.Crit(msg)
			So(buff.String(), ShouldContainSubstring, " lvl=crit msg=msg child=context extra=stuff panic=true")
		})
	})
}

func TestFatal(t *testing.T) {
	msg := "msg"
	if os.Getenv("L15H_TEST_FATAL") == "1" {
		h := log.StreamHandler(os.Stderr, log.LogfmtFormat())
		log.Root().SetHandler(h)
		if os.Getenv("L15H_TEST_FATALCONTEXT") == "1" {
			logger := log.New("child", "context")
			l15h.FatalContext(logger, msg, "extra", "stuff")
		} else {
			l15h.Fatal(msg)
		}
		return
	}

	Convey("You can Fatal() at the root level", t, func() {
		// confirm it normally exits non-zero
		cmd := exec.Command(os.Args[0], "-test.run=TestFatal")
		cmd.Env = append(os.Environ(), "L15H_TEST_FATAL=1")
		out, err := cmd.CombinedOutput()
		e, ok := err.(*exec.ExitError)
		So(ok && !e.Success(), ShouldBeTrue)
		So(string(out), ShouldContainSubstring, " lvl=crit msg=msg fatal=true")

		// get test coverage
		buff := new(bytes.Buffer)
		h := log.StreamHandler(buff, log.LogfmtFormat())
		log.Root().SetHandler(h)

		var i int
		l15h.SetExitFunc(func(code int) {
			i = code
		})

		l15h.Fatal(msg)
		So(i, ShouldEqual, 1)
		So(buff.String(), ShouldContainSubstring, " lvl=crit msg=msg fatal=true")

		Convey("And on a logger with context", func() {
			cmd = exec.Command(os.Args[0], "-test.run=TestFatal")
			cmd.Env = append(os.Environ(), "L15H_TEST_FATAL", "L15H_TEST_FATALCONTEXT=1")
			out, err = cmd.CombinedOutput()
			e, ok = err.(*exec.ExitError)
			So(ok && !e.Success(), ShouldBeTrue)
			So(string(out), ShouldContainSubstring, " lvl=crit msg=msg child=context extra=stuff fatal=true")
		})
	})
}

// Client is our fake smtp client, code taken straight from
// https://github.com/go-playground/log/blob/master/handlers/email/email_test.go
// type Client struct {
// 	conn    net.Conn
// 	address string
// 	time    int64
// 	bufin   *bufio.Reader
// 	bufout  *bufio.Writer
// }

// func (c *Client) w(s string) {
// 	c.bufout.WriteString(s + "\r\n")
// 	c.bufout.Flush()
// }

// func (c *Client) r() string {
// 	reply, err := c.bufin.ReadString('\n')
// 	if err != nil {
// 		fmt.Println("e ", err)
// 	}
// 	return reply
// }

// func handleClient(c *Client, closePrematurly bool) string {
// 	var msg []byte

// 	c.w("220 Welcome to the Jungle")
// 	msg = append(msg, c.r()...)

// 	c.w("250 No one says helo anymore")
// 	msg = append(msg, c.r()...)

// 	c.w("250 Sender")
// 	msg = append(msg, c.r()...)

// 	c.w("250 Recipient")
// 	msg = append(msg, c.r()...)

// 	c.w("354 Ok Send data ending with <CRLF>.<CRLF>")
// 	for {
// 		text := c.r()
// 		bytes := []byte(text)
// 		msg = append(msg, bytes...)

// 		// 46 13 10
// 		if bytes[0] == 46 && bytes[1] == 13 && bytes[2] == 10 {
// 			break
// 		}
// 	}

// 	if !closePrematurly {
// 		c.w("250 server has transmitted the message")
// 	}

// 	c.conn.Close()

// 	return string(msg)
// }

// func TestLogEmail(t *testing.T) {
// 	// create a fake smtp server listening on port 3041
// 	var email string

// 	server, err := net.Listen("tcp", ":3041")
// 	if err != nil {
// 		t.Errorf("Expected <nil> Got '%s'", err)
// 		return
// 	}
// 	defer server.Close()

// 	proceed := make(chan bool)
// 	defer close(proceed)

// 	go func() {
// 		for {
// 			conn, err := server.Accept()
// 			if err != nil {
// 				email = ""
// 				break
// 			}

// 			if conn == nil {
// 				continue
// 			}

// 			c := &Client{
// 				conn:    conn,
// 				address: conn.RemoteAddr().String(),
// 				time:    time.Now().Unix(),
// 				bufin:   bufio.NewReader(conn),
// 				bufout:  bufio.NewWriter(conn),
// 			}

// 			email = handleClient(c, false)

// 			proceed <- true
// 		}
// 	}()

// 	SetAppName("multi")
// 	ToEmail(DebugAndHigher, "localhost", 3041, "", "", "from@email.com", []string{"to@email.com"}, "")
// 	msg := "test msg"

// 	Convey("When logging to email works with an app name set", t, func() {
// 		Convey("Debug(msg) works", func() {
// 			Debug(msg)

// 			<-proceed

// 			expectedPrefix := `EHLO localhost\s+MAIL FROM:<from@email.com>\s+RCPT TO:<to@email.com>`
// 			//*** for some reason ShouldStartWith doesn't work here with a
// 			// literal version of above, don't know why
// 			prefixR := regexp.MustCompile(expectedPrefix)
// 			So(prefixR.MatchString(email), ShouldBeTrue)
// 			So(email, ShouldContainSubstring, "From: from@email.com")
// 			So(email, ShouldContainSubstring, "To: to@email.com")
// 			So(email, ShouldContainSubstring, "Subject: test msg")
// 			So(email, ShouldContainSubstring, "<h2>test msg</h2>")
// 			So(email, ShouldContainSubstring, "<h4>multi</h4>") // *** this randomly fails!
// 			So(email, ShouldContainSubstring, "<p>DEBUG</p>")
// 		})

// 		Convey("Info(msg) works", func() {
// 			Info(msg)
// 			<-proceed
// 			So(email, ShouldContainSubstring, "<p>INFO</p>")
// 		})

// 		// *** Panic() creates an unreadable mess in emails atm...
// 	})
// }
