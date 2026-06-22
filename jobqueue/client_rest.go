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
	"os"
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
func (c *Client) GetSchedulerAlerts() (*SchedulerAlerts, error) {
	alerts := &SchedulerAlerts{}
	if err := c.restGet(restBadServersEndpoint, &alerts.BadServers); err != nil {
		return nil, err
	}

	if err := c.restGet(restWarningsEndpoint, &alerts.Issues); err != nil {
		return nil, err
	}

	return alerts, nil
}

func (c *Client) restGet(endpoint string, response any) error {
	url, err := c.restURL(endpoint)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", bearerSchema+string(c.token))

	resp, err := c.restHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err = restCheckStatus(endpoint, resp); err != nil {
		return err
	}

	decoder := json.NewDecoder(resp.Body)

	return decoder.Decode(response)
}

func (c *Client) restURL(endpoint string) (string, error) {
	if c.ServerInfo == nil || c.ServerInfo.WebPort == "" {
		return "", errSchedulerAlertsNoServerInfo
	}

	host := c.host
	if host == "" {
		host = c.ServerInfo.Host
	}

	return "https://" + net.JoinHostPort(host, c.ServerInfo.WebPort) + endpoint, nil
}

func (c *Client) restHTTPClient() *http.Client {
	c.Lock()
	defer c.Unlock()

	if c.restClient == nil {
		c.restClient = c.newRestHTTPClient()
	}

	return c.restClient
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
