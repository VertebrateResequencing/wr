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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

const (
	defaultRESTClientTimeout = 30 * time.Second
	restErrorReadLimit       = 4096
	restClientArgsCAFile     = 1
	restClientArgsCertDomain = 2
)

var (
	errSchedulerAlertsNoServerInfo = errors.New("client has no server web interface details")
	errRESTUnexpectedStatus        = errors.New("REST request returned unexpected status")
)

func restCheckStatus(endpoint string, resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, restErrorReadLimit))
	if err != nil {
		return fmt.Errorf("%w: failed to read GET %s response: %w", errRESTUnexpectedStatus, endpoint, err)
	}

	return fmt.Errorf("%w: GET %s returned %s: %s", errRESTUnexpectedStatus,
		endpoint, resp.Status, string(bytes.TrimSpace(body)))
}

func restRootCAPool(caFile string) *x509.CertPool {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caCert) {
		return nil
	}

	return certPool
}

// GetSchedulerAlerts returns scheduler issues and bad cloud servers currently
// exposed by the manager web API. Reading Issues dismisses them on the manager,
// matching the existing warnings REST endpoint behaviour used by the web UI.
//
// If the manager rejects the token, a client made with ConnectWithTokenFile
// reloads it as other requests do. A client that rides out outages (see
// RetryWhileManagerUnreachable) sends a request again only if no copy of it
// was written to the manager's web interface, as when it could not connect, or
// if the manager rejected its token while its token file held none, as while a
// manager that stopped cleanly is starting again. One that may have reached
// it, such as one whose reply did not arrive in time or whose connection
// dropped, is not sent again, since the manager may have dismissed the issues
// it was reading; its error is returned at once. The request for the issues
// always goes on a new connection, because net/http itself sends a GET again
// if a reused connection drops it, even after writing it.
func (c *Client) GetSchedulerAlerts() (*SchedulerAlerts, error) {
	return c.GetSchedulerAlertsContext(context.Background())
}

// GetSchedulerAlertsContext is GetSchedulerAlerts, except that on a client that
// rides out outages it stops retrying once ctx is done, as GetByEssenceContext
// does. A request in progress is not interrupted, since the manager may then
// have dismissed issues whose reply never arrives, but on such a client no
// further request is started once ctx is done.
func (c *Client) GetSchedulerAlertsContext(ctx context.Context) (*SchedulerAlerts, error) {
	alerts := &SchedulerAlerts{}
	if err := c.restGet(ctx, restBadServersEndpoint, false, &alerts.BadServers); err != nil {
		return nil, err
	}

	if err := c.restGet(ctx, restWarningsEndpoint, true, &alerts.Issues); err != nil {
		return nil, err
	}

	return alerts, nil
}

// restGet GETs endpoint and decodes its JSON reply into response, riding out an
// outage until ctx is done if this client does that. If acts, the manager acts
// on the GET (as on restWarningsEndpoint), so it is sent on a new connection
// (see restHTTPClientNoReuse).
func (c *Client) restGet(ctx context.Context, endpoint string, acts bool, response any) error {
	retry := c.outageRetry.Load()
	if retry == nil {
		_, err := c.restGetOnce(ctx, endpoint, acts, response)

		return err
	}

	return c.rideOutOutage(ctx, retry, http.MethodGet+" "+endpoint, func() (bool, error) {
		return c.restGetOnce(ctx, endpoint, acts, response)
	})
}

// restGetOnce is one attempt at restGet. If the manager rejects the token, and
// the client's token file now holds a different one, it is sent once more with
// that, which is safe because the manager rejects a bad token before acting on
// the request. It reports whether it failed without reaching the manager, or
// was rejected by a manager not yet ready for it (see awaitingTokenFile).
func (c *Client) restGetOnce(ctx context.Context, endpoint string, acts bool, response any) (bool, error) {
	token := c.currentToken()

	resp, unreached, err := c.restDo(ctx, endpoint, acts, token)
	if err != nil {
		return unreached, err
	}

	if resp.StatusCode == http.StatusUnauthorized && c.reloadTokenUnlocked(token) {
		resp.Body.Close()

		resp, unreached, err = c.restDo(ctx, endpoint, acts, c.currentToken())
		if err != nil {
			return unreached, err
		}
	}

	defer resp.Body.Close()

	if err = restCheckStatus(endpoint, resp); err != nil {
		return resp.StatusCode == http.StatusUnauthorized && c.awaitingTokenFile(), err
	}

	return false, json.NewDecoder(resp.Body).Decode(response)
}

// awaitingTokenFile reports whether this client has a token file that does not
// hold a token. A manager that stopped cleanly deleted it, and when started
// again serves its web interface, rejecting every token, before it writes its
// new one, so a rejection then means the manager is not ready yet, not that the
// client is not allowed.
func (c *Client) awaitingTokenFile() bool {
	if c.tokenFile == "" {
		return false
	}

	token, err := os.ReadFile(filepath.Clean(c.tokenFile))

	return err != nil || len(token) != tokenLength
}

// restDo sends a GET of endpoint authenticated with token, on a new connection
// if acts. If it fails, it reports whether the request cannot have reached the
// manager, which is when net/http tried to get a connection for it but did not
// write its headers on any of its tries (it can try again itself on a reused
// connection): until then nothing of it is sent, since a TLS connection is
// only got after its handshake. A failure before any try, such as a bad URL,
// is not a failure to reach the manager. ctx's cancellation is ignored, since
// the manager may have acted on a request whose reply is then never read.
func (c *Client) restDo(ctx context.Context, endpoint string, acts bool,
	token []byte,
) (*http.Response, bool, error) {
	url, err := c.restURL(endpoint)
	if err != nil {
		return nil, false, err
	}

	var tried, wrote atomic.Bool

	trace := &httptrace.ClientTrace{
		GetConn:      func(string) { tried.Store(true) },
		WroteHeaders: func() { wrote.Store(true) },
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.WithoutCancel(ctx), trace),
		http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}

	req.Header.Set("Authorization", bearerSchema+string(token))

	resp, err := c.restHTTPClientFor(acts).Do(req)
	if err != nil {
		return nil, tried.Load() && !wrote.Load(), err
	}

	return resp, false, nil
}

// reloadTokenUnlocked is reloadToken for a caller not holding the client's
// lock.
func (c *Client) reloadTokenUnlocked(rejected []byte) bool {
	c.Lock()
	defer c.Unlock()

	return c.reloadToken(rejected)
}

func (c *Client) restURL(endpoint string) (string, error) {
	si := c.CurrentServerInfo()
	if si == nil || si.WebPort == "" {
		return "", errSchedulerAlertsNoServerInfo
	}

	host := c.host
	if host == "" {
		host = si.Host
	}

	return "https://" + net.JoinHostPort(host, si.WebPort) + endpoint, nil
}

func (c *Client) restHTTPClient() *http.Client {
	c.Lock()
	defer c.Unlock()

	if c.restClient == nil {
		c.restClient = c.newRestHTTPClient()
	}

	return c.restClient
}

// restHTTPClientFor returns restHTTPClientNoReuse() for a request the manager
// acts on, otherwise restHTTPClient().
func (c *Client) restHTTPClientFor(acts bool) *http.Client {
	if acts {
		return c.restHTTPClientNoReuse()
	}

	return c.restHTTPClient()
}

// restHTTPClientNoReuse is restHTTPClient, except that it sends every request
// on a new connection. net/http's Transport sends a GET again itself only if a
// connection it reused for it fails (see its shouldRetryRequest), which can be
// after the manager got and acted on the first copy.
func (c *Client) restHTTPClientNoReuse() *http.Client {
	base := c.restHTTPClient()

	c.Lock()
	defer c.Unlock()

	if c.restClientNoReuse == nil {
		client := *base

		if transport, ok := base.Transport.(*http.Transport); ok {
			transport = transport.Clone()
			transport.DisableKeepAlives = true
			client.Transport = transport
		}

		c.restClientNoReuse = &client
	}

	return c.restClientNoReuse
}

func (c *Client) newRestHTTPClient() *http.Client {
	return &http.Client{
		Timeout: c.restTimeout(),
		Transport: &http.Transport{
			Proxy:           nil,
			TLSClientConfig: c.restTLSConfig(),
		},
	}
}

func (c *Client) restTimeout() time.Duration {
	if c.timeout > 0 && c.timeout < defaultRESTClientTimeout {
		return c.timeout
	}

	return defaultRESTClientTimeout
}

func (c *Client) restTLSConfig() *tls.Config {
	caFile, certDomain := c.restTLSConfigInputs()

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: certDomain}

	if certPool := restRootCAPool(caFile); certPool != nil {
		tlsConfig.RootCAs = certPool
	}

	return tlsConfig
}

func (c *Client) restTLSConfigInputs() (string, string) {
	caFile := ""
	certDomain := ""

	if len(c.args) > restClientArgsCAFile {
		caFile = c.args[restClientArgsCAFile]
	}

	if len(c.args) > restClientArgsCertDomain {
		certDomain = c.args[restClientArgsCertDomain]
	}

	return caFile, certDomain
}
