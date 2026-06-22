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
	crand "crypto/rand"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const restTestCertDomain = "manager-cert.example.org"

func TestRESTHTTPClientReuse(t *testing.T) {
	Convey("REST calls reuse an HTTP client that ignores proxy environment variables", t, func() {
		t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

		jq := &Client{
			timeout: time.Second,
			args:    []string{localhost + ":0", "", localhost},
		}
		client := jq.restHTTPClient()
		transport, ok := client.Transport.(*http.Transport)

		So(jq.restHTTPClient() == client, ShouldBeTrue)
		So(ok, ShouldBeTrue)
		So(transport.Proxy, ShouldBeNil)
	})
}

func TestRESTURLUsesConnectedHost(t *testing.T) {
	Convey("REST URLs prefer the host used for the RPC connection", t, func() {
		jq := &Client{
			ServerInfo: &ServerInfo{
				Host:    restTestCertDomain,
				WebPort: "1234",
			},
			host: "127.0.0.1",
		}

		url, err := jq.restURL("/api")

		So(err, ShouldBeNil)
		So(url, ShouldEqual, "https://127.0.0.1:1234/api")
	})

	Convey("REST URLs fall back to the server host when no connected host is known", t, func() {
		jq := &Client{
			ServerInfo: &ServerInfo{
				Host:    restTestCertDomain,
				WebPort: "1234",
			},
		}

		url, err := jq.restURL("/api")

		So(err, ShouldBeNil)
		So(url, ShouldEqual, "https://"+restTestCertDomain+":1234/api")
	})
}

func TestRESTTLSConfigCAPool(t *testing.T) {
	Convey("REST TLS config uses system roots when the configured CA file has no PEM certificates", t, func() {
		caFile := filepath.Join(t.TempDir(), "ca.pem")
		So(os.WriteFile(caFile, []byte("not a certificate"), 0o600), ShouldBeNil)

		jq := &Client{args: []string{localhost + ":0", caFile, restTestCertDomain}}

		tlsConfig := jq.restTLSConfig()

		So(tlsConfig.ServerName, ShouldEqual, restTestCertDomain)
		So(tlsConfig.RootCAs, ShouldBeNil)
	})

	Convey("REST TLS config uses a custom root pool when the configured CA file contains PEM certificates", t, func() {
		certDir := t.TempDir()
		caFile := filepath.Join(certDir, "ca.pem")
		certFile := filepath.Join(certDir, "cert.pem")
		keyFile := filepath.Join(certDir, "key.pem")

		err := internal.GenerateCerts(caFile, certFile, keyFile, localhost, internal.DefaultBitsForRootRSAKey,
			internal.DefualtBitsForServerRSAKey, crand.Reader, internal.DefaultCertFileFlags)
		So(err, ShouldBeNil)

		jq := &Client{args: []string{localhost + ":0", caFile, localhost}}

		tlsConfig := jq.restTLSConfig()

		So(tlsConfig.RootCAs, ShouldNotBeNil)
	})
}
