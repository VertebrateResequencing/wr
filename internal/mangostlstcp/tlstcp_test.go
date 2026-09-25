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

package mangostlstcp

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol/rep"
	"go.nanomsg.org/mangos/v3/protocol/req"
)

// testHandshakeTimeout is the handshake bound the tests configure: short, so a
// test can wait out a few of them in a second or two.
const testHandshakeTimeout = 300 * time.Millisecond

// testDropLimit is how long a listener is given to drop a silent client:
// comfortably longer than testHandshakeTimeout, and far shorter than forever.
const testDropLimit = testHandshakeTimeout + 3*time.Second

// TestHandshakeTimeout proves that the handshake timeout drops a peer that
// connects but never handshakes, and that it applies only to the handshake, so
// an established connection outlives it.
func TestHandshakeTimeout(t *testing.T) {
	Convey("Given a listener with a short handshake timeout", t, func() {
		serverConfig, clientConfig := testTLSConfigs(t)

		sock, err := rep.NewSocket()
		So(err, ShouldBeNil)

		defer sock.Close()

		l, err := sock.NewListener("tls+tcp://localhost:0", map[string]any{
			mangos.OptionTLSConfig: serverConfig,
			OptionHandshakeTimeout: testHandshakeTimeout,
		})
		So(err, ShouldBeNil)
		So(l.Listen(), ShouldBeNil)

		addr := strings.TrimPrefix(l.Address(), "tls+tcp://")

		Convey("a client that connects but never handshakes is dropped", func() {
			var d net.Dialer

			conn, err := d.DialContext(context.Background(), "tcp", addr)
			So(err, ShouldBeNil)

			defer conn.Close()

			So(conn.SetReadDeadline(time.Now().Add(testDropLimit)), ShouldBeNil)

			start := time.Now()
			_, err = conn.Read(make([]byte, 1))
			took := time.Since(start)

			// the listener closed the connection, rather than our own read
			// deadline giving up waiting for it to
			So(err, ShouldNotBeNil)
			So(errors.Is(err, os.ErrDeadlineExceeded), ShouldBeFalse)
			So(took, ShouldBeLessThan, testDropLimit)
		})

		Convey("a connection that completes its handshakes still carries messages after the timeout", func() {
			client, err := req.NewSocket()
			So(err, ShouldBeNil)

			defer client.Close()

			So(client.SetOption(mangos.OptionRecvDeadline, testDropLimit), ShouldBeNil)
			So(client.SetOption(mangos.OptionSendDeadline, testDropLimit), ShouldBeNil)
			So(sock.SetOption(mangos.OptionRecvDeadline, testDropLimit), ShouldBeNil)

			// a redial would hide a connection that died at the timeout, since
			// req resends on the new one, so don't let one happen in time
			So(client.DialOptions("tls+tcp://"+addr, map[string]any{
				mangos.OptionTLSConfig:     clientConfig,
				OptionHandshakeTimeout:     testHandshakeTimeout,
				mangos.OptionReconnectTime: time.Hour,
			}), ShouldBeNil)

			<-time.After(2 * testHandshakeTimeout)

			So(client.Send([]byte("ping")), ShouldBeNil)

			msg, err := sock.Recv()
			So(err, ShouldBeNil)
			So(string(msg), ShouldEqual, "ping")

			So(sock.Send([]byte("pong")), ShouldBeNil)

			msg, err = client.Recv()
			So(err, ShouldBeNil)
			So(string(msg), ShouldEqual, "pong")
		})
	})
}

// testTLSConfigs generates a CA and a localhost server certificate, returning
// a server TLS config that presents the certificate and a client TLS config
// that trusts the CA.
func testTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	err := internal.GenerateCerts(caFile, certFile, keyFile, "localhost", 2048, 2048,
		rand.Reader, os.O_RDWR|os.O_CREATE|os.O_TRUNC)
	So(err, ShouldBeNil)

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	So(err, ShouldBeNil)

	caPEM, err := os.ReadFile(caFile)
	So(err, ShouldBeNil)

	pool := x509.NewCertPool()
	So(pool.AppendCertsFromPEM(caPEM), ShouldBeTrue)

	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}
}
