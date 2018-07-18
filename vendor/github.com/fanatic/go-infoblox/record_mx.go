package infoblox

import "fmt"

// RecordMx returns the MX record resource
func (c *Client) RecordMx() *Resource {
	return &Resource{
		conn:       c,
		wapiObject: "record:mx",
	}
}

// RecordMxObject defines the MX record object's fields
type RecordMxObject struct {
	Object
	Comment   string `json:"comment,omitempty"`
	Exchanger string `json:"exchanger,omitempty"`
	Name      string `json:"name,omitempty"`
	Pref      int    `json:"pref,omitempty"`
	Ttl       int    `json:"ttl,omitempty"`
	View      string `json:"view,omitempty"`
}

// RecordMxObject instantiates an MX record object with a WAPI ref
func (c *Client) RecordMxObject(ref string) *RecordMxObject {
	a := RecordMxObject{}
	a.Object = Object{
		Ref: ref,
		r:   c.RecordMx(),
	}
	return &a
}

// GetRecordMx fetches an MX record from the Infoblox WAPI by its ref
func (c *Client) GetRecordMx(ref string, opts *Options) (*RecordMxObject, error) {
	resp, err := c.RecordMxObject(ref).get(opts)
	if err != nil {
		return nil, fmt.Errorf("Could not get created MX record: %s", err)
	}
	var out RecordMxObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
