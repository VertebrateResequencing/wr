package infoblox

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
)

const (
	statusOK           = 200
	statusCreated      = 201
	statusInvalid      = 400
	statusUnauthorized = 401
	statusForbidden    = 403
	statusNotFound     = 404
	statusLimit        = 429
	statusGateway      = 502
)

// APIResponse is used to parse the response from Infoblox.
// GET requests tend to respond with Objects or lists of Objects
// while POST,PUT,DELETE returns the Object References as a string
// https://192.168.2.200/wapidoc/#get
type APIResponse http.Response

func (r APIResponse) readBody() ([]byte, error) {
	defer r.Body.Close()

	// The default HTTP client now handles decompression for us, but
	// this is left for backwards compatability
	header := strings.ToLower(r.Header.Get("Content-Encoding"))
	if header == "" || strings.Index(header, "gzip") == -1 {
		return ioutil.ReadAll(r.Body)
	}

	reader, err := gzip.NewReader(r.Body)
	if err != nil {
		return nil, err
	}
	return ioutil.ReadAll(reader)
}

// ReadBody returns the body of the response as a string, or returns the empty
// string if there is an error
func (r APIResponse) ReadBody() string {
	if b, err := r.readBody(); err == nil {
		return string(b)
	}
	return ""
}

// Parse parses a JSON encoded HTTP response into the supplied interface.
func (r APIResponse) Parse(out interface{}) error {
	switch r.StatusCode {
	case statusUnauthorized:
		fallthrough
	case statusNotFound:
		fallthrough
	case statusGateway:
		fallthrough
	case statusForbidden:
		fallthrough
	case statusInvalid:
		b, err := r.readBody()
		if err != nil {
			return err
		}
		e := Error{}
		if err := json.Unmarshal(b, &e); err != nil {
			return fmt.Errorf("Error parsing error response: %v", string(b))
		}
		return e
	//case STATUS_LIMIT:
	//  err = RateLimitError{
	//    Limit:     r.RateLimit(),
	//    Remaining: r.RateLimitRemaining(),
	//    Reset:     r.RateLimitReset(),
	//  }
	//  return
	case statusCreated:
		fallthrough
	case statusOK:
		b, err := r.readBody()
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, out); err != nil && err != io.EOF {
			return err
		}
	default:
		b, err := r.readBody()
		if err != nil {
			return err
		}
		return fmt.Errorf("%v", string(b))
	}
	return nil
}
