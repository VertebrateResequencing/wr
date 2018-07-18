// Package infoblox implements an Infoblox DNS/DHCP appliance client library in Go
package infoblox

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

var (
	// WapiVersion specifies the version of the Infoblox REST API to target
	WapiVersion = "1.4.1"

	// BasePath specifies the default path prefix to all WAPI actions
	BasePath = "/wapi/v" + WapiVersion + "/"

	// Debug mode flag
	Debug = false
)

// Client implements a Infoblox WAPI client.
// https://192.168.2.200/wapidoc/#transport-and-authentication
type Client struct {
	Host       string
	Password   string
	Username   string
	HTTPClient *http.Client
	UseCookies bool
}

// NewClient instantiates a new Infoblox client with the supplied user/pass configuration.
// Supports the use of HTTP proxies through the $HTTP_PROXY env var.
// For example:
//     export HTTP_PROXY=http://localhost:8888
//
// When using a proxy, disable TLS certificate verification with the following:
//    sslVerify = false
//
// To save and re-use infoblox session cookies, set useCookies = true
// NOTE: The infoblox cookie uses a comma separated string, and requires golang 1.3+ to be correctly stored.
func NewClient(host, username, password string, sslVerify, useCookies bool) *Client {
	var (
		req, _    = http.NewRequest("GET", host, nil)
		proxy, _  = http.ProxyFromEnvironment(req)
		transport *http.Transport
		tlsconfig *tls.Config
	)
	tlsconfig = &tls.Config{
		InsecureSkipVerify: !sslVerify,
	}
	if tlsconfig.InsecureSkipVerify {
		log.Printf("WARNING: SSL cert verification  disabled\n")
	}
	transport = &http.Transport{
		TLSClientConfig: tlsconfig,
	}
	if proxy != nil {
		transport.Proxy = http.ProxyURL(proxy)
	}

	client := &Client{
		Host: host,
		HTTPClient: &http.Client{
			Transport: transport,
		},
		Username:   username,
		Password:   password,
		UseCookies: useCookies,
	}
	if useCookies {
		options := cookiejar.Options{
			PublicSuffixList: publicsuffix.List,
		}
		jar, _ := cookiejar.New(&options)
		client.HTTPClient.Jar = jar
	}
	return client

}

// SendRequest sends a HTTP request through this instance's HTTP client.
// Uses cookies if specified, re-creating the request and falling back to basic auth if a cookie is not present
func (c *Client) SendRequest(method, urlStr, body string, head map[string]string) (resp *APIResponse, err error) {
	log.Printf("%s %s  payload: %s\n", method, urlStr, body)
	req, err := c.buildRequest(method, urlStr, body, head)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}
	var r *http.Response
	if !c.UseCookies {
		// Go right to basic auth if we arent using cookies
		req.SetBasicAuth(c.Username, c.Password)
	}

	r, err = c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error executing request: %v", err)
	}
	if r.StatusCode == 401 && c.UseCookies { // don't bother re-sending if we aren't using cookies
		log.Printf("Re-sending request with basic auth after 401")
		// Re-build request
		req, err = c.buildRequest(method, urlStr, body, head)
		if err != nil {
			return nil, fmt.Errorf("error re-creating request: %v", err)
		}
		// Set basic auth
		req.SetBasicAuth(c.Username, c.Password)
		// Resend request
		r, err = c.HTTPClient.Do(req)
	}
	resp = (*APIResponse)(r)
	return
}

// build a new http request from this client
func (c *Client) buildRequest(method, urlStr, body string, head map[string]string) (*http.Request, error) {
	var req *http.Request
	var err error
	if body == "" {
		req, err = http.NewRequest(method, urlStr, nil)
	} else {
		b := strings.NewReader(body)
		req, err = http.NewRequest(method, urlStr, b)
	}
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(urlStr, "http") {
		u := fmt.Sprintf("%v%v", c.Host, urlStr)
		req.URL, err = url.Parse(u)
		if err != nil {
			return nil, err
		}
	}
	for k, v := range head {
		req.Header.Set(k, v)
	}
	return req, err
}
