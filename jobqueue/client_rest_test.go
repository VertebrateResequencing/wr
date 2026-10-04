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
	"context"
	crand "crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const restTestCertDomain = "manager-cert.example.org"

const (
	alertsTestTimeout   = 300 * time.Millisecond
	alertsTestRetryWait = 50 * time.Millisecond
	alertsTestRetryTime = time.Second
	alertsTestOldToken  = "old-token-old-token-old-token-old-token-old"
	alertsTestNewToken  = "new-token-new-token-new-token-new-token-new"
)

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

func TestRESTHTTPClientTimeout(t *testing.T) {
	Convey("REST calls cap long RPC timeouts at the default REST client timeout", t, func() {
		jq := &Client{timeout: 2 * defaultRESTClientTimeout}

		client := jq.restHTTPClient()

		So(client.Timeout, ShouldEqual, defaultRESTClientTimeout)
	})

	Convey("REST calls preserve RPC timeouts shorter than the default REST client timeout", t, func() {
		shorterTimeout := defaultRESTClientTimeout / 3
		jq := &Client{timeout: shorterTimeout}

		client := jq.restHTTPClient()

		So(client.Timeout, ShouldEqual, shorterTimeout)
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

// alertsTestManager is a stand-in for the manager's web interface that counts
// the requests it gets and answers each with answer.
type alertsTestManager struct {
	server *httptest.Server
	hits   atomic.Int32
	answer func(w http.ResponseWriter, r *http.Request)
}

func newAlertsTestManager(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *alertsTestManager {
	t.Helper()

	m := &alertsTestManager{answer: answer}
	m.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.hits.Add(1)
		m.answer(w, r)
	}))
	t.Cleanup(m.server.Close)

	return m
}

// client returns a Client whose REST requests go to m with alertsTestOldToken,
// opted in to riding out outages if retry.
func (m *alertsTestManager) client(retry bool) *Client {
	u, err := url.Parse(m.server.URL)
	So(err, ShouldBeNil)

	host, port, err := net.SplitHostPort(u.Host)
	So(err, ShouldBeNil)

	httpClient := m.server.Client()
	httpClient.Timeout = alertsTestTimeout

	jq := &Client{
		ServerInfo: &ServerInfo{Host: host, WebPort: port},
		host:       host,
		timeout:    alertsTestTimeout,
		restClient: httpClient,
		token:      []byte(alertsTestOldToken),
	}

	setOutageTimings(jq, alertsTestRetryWait, alertsTestRetryTime)

	if retry {
		jq.RetryWhileManagerUnreachable(context.Background())
	}

	return jq
}

// TestClientGetSchedulerAlertsOutage checks that a Client that rides out
// outages sends a GetSchedulerAlerts again only if it cannot have reached the
// manager, since reading the alerts dismisses them.
func TestClientGetSchedulerAlertsOutage(t *testing.T) {
	Convey("Given a manager web interface", t, func() {
		Convey("a Client that opted in does not resend a request whose connection was dropped after it was sent", func() {
			m := newAlertsTestManager(t, func(w http.ResponseWriter, _ *http.Request) { dropConnection(w) })

			calledAt := time.Now()
			_, err := m.client(true).GetSchedulerAlerts()

			So(err, ShouldNotBeNil)
			So(time.Since(calledAt), ShouldBeLessThan, alertsTestRetryTime)
			So(m.hits.Load(), ShouldEqual, 1)
		})

		Convey("a Client that opted in does not resend a request whose reply did not arrive in time", func() {
			release := make(chan struct{})
			defer close(release)

			m := newAlertsTestManager(t, func(_ http.ResponseWriter, _ *http.Request) { <-release })

			calledAt := time.Now()
			_, err := m.client(true).GetSchedulerAlerts()

			So(errors.Is(err, context.DeadlineExceeded), ShouldBeTrue)
			So(time.Since(calledAt), ShouldBeLessThan, alertsTestRetryTime)
			So(m.hits.Load(), ShouldEqual, 1)
		})

		Convey("a Client never sends a warnings request again after the connection it was sent on dropped", func() {
			var warningsHits atomic.Int32

			m := newAlertsTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != restWarningsEndpoint {
					answerEmpty(w, r)

					return
				}

				warningsHits.Add(1)
				dropConnection(w)
			})

			for _, retry := range []bool{true, false} {
				warningsHits.Store(0)

				calledAt := time.Now()
				_, err := m.client(retry).GetSchedulerAlerts()

				So(err, ShouldNotBeNil)
				So(time.Since(calledAt), ShouldBeLessThan, alertsTestRetryTime)
				So(warningsHits.Load(), ShouldEqual, 1)
			}
		})

		Convey("a Client does not send a warnings request on a connection it kept alive from an earlier call", func() {
			var warningsHits atomic.Int32

			m := newAlertsTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == restWarningsEndpoint && warningsHits.Add(1) == 2 {
					dropConnection(w)

					return
				}

				answerEmpty(w, r)
			})

			jq := m.client(false)

			_, err := jq.GetSchedulerAlerts()
			So(err, ShouldBeNil)

			_, err = jq.GetSchedulerAlerts()
			So(err, ShouldNotBeNil)
			So(warningsHits.Load(), ShouldEqual, 2)
		})

		Convey("a Client that opted in does not resend a warnings request whose connection dropped as the "+
			"manager went away", func() {
			var m *alertsTestManager

			m = newAlertsTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				if m.hits.Load() <= 3 {
					answerEmpty(w, r)

					return
				}

				m.server.Listener.Close()
				dropConnection(w)
			})

			jq := m.client(true)

			_, err := jq.GetSchedulerAlerts()
			So(err, ShouldBeNil)

			calledAt := time.Now()
			_, err = jq.GetSchedulerAlerts()

			So(err, ShouldNotBeNil)
			So(time.Since(calledAt), ShouldBeLessThan, alertsTestRetryTime)
			So(m.hits.Load(), ShouldEqual, 4)
		})

		Convey("a Client that opted in does not interrupt a request in progress when the context of the call "+
			"is done", func() {
			arrived := make(chan struct{}, 1)
			release := make(chan struct{})

			// the warnings request, which dismisses the issues it reads, is the
			// one in progress
			m := newAlertsTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == restWarningsEndpoint {
					arrived <- struct{}{}

					<-release
				}

				answerEmpty(w, r)
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			jq := m.client(true)
			got := make(chan error, 1)

			go func() {
				_, err := jq.GetSchedulerAlertsContext(ctx)
				got <- err
			}()

			<-arrived
			cancel()
			close(release)

			So(<-got, ShouldBeNil)
			So(m.hits.Load(), ShouldEqual, 2)
		})

		Convey("once it is gone", func() {
			m := newAlertsTestManager(t, answerEmpty)
			m.server.Close()

			Convey("a Client that opted in keeps trying for its RetryTime", func() {
				calledAt := time.Now()
				_, err := m.client(true).GetSchedulerAlerts()

				So(errors.Is(err, syscall.ECONNREFUSED), ShouldBeTrue)
				So(time.Since(calledAt), ShouldBeGreaterThanOrEqualTo, alertsTestRetryTime)
			})

			Convey("a Client that opted in stops trying once the context of the call is done", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*alertsTestRetryWait)
				defer cancel()

				calledAt := time.Now()
				_, err := m.client(true).GetSchedulerAlertsContext(ctx)

				So(errors.Is(err, context.DeadlineExceeded), ShouldBeTrue)
				So(errors.Is(err, syscall.ECONNREFUSED), ShouldBeTrue)
				So(time.Since(calledAt), ShouldBeLessThan, alertsTestRetryTime)
			})

			Convey("a Client that did not opt in fails at once", func() {
				calledAt := time.Now()
				_, err := m.client(false).GetSchedulerAlerts()

				So(errors.Is(err, syscall.ECONNREFUSED), ShouldBeTrue)
				So(time.Since(calledAt), ShouldBeLessThan, alertsTestTimeout)
			})
		})
	})
}

// dropConnection closes the connection w would answer on, without answering.
func dropConnection(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return
	}

	if conn, _, err := hijacker.Hijack(); err == nil {
		conn.Close()
	}
}

// answerEmpty answers a GET of the alerts endpoints with no alerts.
func answerEmpty(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("[]")) //nolint:errcheck
}

// TestClientGetSchedulerAlertsTokenReload checks that GetSchedulerAlerts,
// like the other requests, reloads a token the manager rejects from the
// client's token file.
func TestClientGetSchedulerAlertsTokenReload(t *testing.T) {
	Convey("Given a manager web interface that only accepts a new token", t, func() {
		m := newAlertsTestManager(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != bearerSchema+alertsTestNewToken {
				http.Error(w, "Invalid token", http.StatusUnauthorized)

				return
			}

			answerEmpty(w, r)
		})

		tokenFile := filepath.Join(t.TempDir(), "client.token")
		jq := m.client(false)

		Convey("a Client with the old token whose token file now holds the new one reloads it and succeeds", func() {
			So(os.WriteFile(tokenFile, []byte(alertsTestNewToken), 0o600), ShouldBeNil)

			jq.tokenFile = tokenFile

			alerts, err := jq.GetSchedulerAlerts()

			So(err, ShouldBeNil)
			So(alerts, ShouldNotBeNil)
			So(string(jq.currentToken()), ShouldEqual, alertsTestNewToken)
			So(jq.tokenReloads, ShouldEqual, 1)
			So(m.hits.Load(), ShouldEqual, 3)
		})

		Convey("a Client whose token file still holds the old token is refused", func() {
			So(os.WriteFile(tokenFile, []byte(alertsTestOldToken), 0o600), ShouldBeNil)

			jq.tokenFile = tokenFile

			_, err := jq.GetSchedulerAlerts()

			So(errors.Is(err, errRESTUnexpectedStatus), ShouldBeTrue)
			So(m.hits.Load(), ShouldEqual, 1)
		})

		Convey("a Client that opted in whose token file is missing keeps trying until the manager writes it", func() {
			jq.tokenFile = tokenFile
			jq.RetryWhileManagerUnreachable(context.Background())

			go func() {
				<-time.After(3 * alertsTestRetryWait)
				os.WriteFile(tokenFile, []byte(alertsTestNewToken), 0o600) //nolint:errcheck
			}()

			_, err := jq.GetSchedulerAlerts()

			So(err, ShouldBeNil)
			So(string(jq.currentToken()), ShouldEqual, alertsTestNewToken)
			So(m.hits.Load(), ShouldBeGreaterThanOrEqualTo, 3)
		})

		Convey("a Client that opted in whose token file still holds the old token is refused at once", func() {
			So(os.WriteFile(tokenFile, []byte(alertsTestOldToken), 0o600), ShouldBeNil)

			jq.tokenFile = tokenFile
			jq.RetryWhileManagerUnreachable(context.Background())

			_, err := jq.GetSchedulerAlerts()

			So(errors.Is(err, errRESTUnexpectedStatus), ShouldBeTrue)
			So(m.hits.Load(), ShouldEqual, 1)
		})

		Convey("a Client that did not opt in whose token file is missing is refused at once", func() {
			jq.tokenFile = tokenFile

			_, err := jq.GetSchedulerAlerts()

			So(errors.Is(err, errRESTUnexpectedStatus), ShouldBeTrue)
			So(m.hits.Load(), ShouldEqual, 1)
		})

		Convey("a Client with no token file is refused", func() {
			_, err := jq.GetSchedulerAlerts()

			So(errors.Is(err, errRESTUnexpectedStatus), ShouldBeTrue)
			So(m.hits.Load(), ShouldEqual, 1)
		})
	})
}
