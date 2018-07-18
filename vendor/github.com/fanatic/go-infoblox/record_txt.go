package infoblox

import "fmt"

// RecordTxt returns the TXT record resource
func (c *Client) RecordTxt() *Resource {
	return &Resource{
		conn:       c,
		wapiObject: "record:txt",
	}
}

// RecordTxtObject defines the TXT record object's fields
type RecordTxtObject struct {
	Object
	Comment string `json:"comment,omitempty"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text,omitempty"`
	Ttl     int    `json:"ttl,omitempty"`
	View    string `json:"view,omitempty"`
}

// RecordTxtObject instantiates a TXT record object with a WAPI ref
func (c *Client) RecordTxtObject(ref string) *RecordTxtObject {
	a := RecordTxtObject{}
	a.Object = Object{
		Ref: ref,
		r:   c.RecordTxt(),
	}
	return &a
}

// GetRecordTxt fetches a TXT record from the Infoblox WAPI by its ref
func (c *Client) GetRecordTxt(ref string, opts *Options) (*RecordTxtObject, error) {
	resp, err := c.RecordTxtObject(ref).get(opts)
	if err != nil {
		return nil, fmt.Errorf("Could not get created TXT record: %s", err)
	}
	var out RecordTxtObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
