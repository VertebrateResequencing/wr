package infoblox

import "fmt"

// RecordA returns the A record resource
// https://102.168.2.200/wapidoc/objects/record.a.html
func (c *Client) RecordA() *Resource {
	return &Resource{
		conn:       c,
		wapiObject: "record:a",
	}
}

// RecordAObject defines the A record object's fields
type RecordAObject struct {
	Object
	Comment  string `json:"comment,omitempty"`
	Ipv4Addr string `json:"ipv4addr,omitempty"`
	Name     string `json:"name,omitempty"`
	Ttl      int    `json:"ttl,omitempty"`
	View     string `json:"view,omitempty"`
}

// RecordAObject instantiates an A record object with a WAPI ref
func (c *Client) RecordAObject(ref string) *RecordAObject {
	a := RecordAObject{}
	a.Object = Object{
		Ref: ref,
		r:   c.RecordA(),
	}
	return &a
}

// GetRecordA fetches an A record from the Infoblox WAPI by its ref
func (c *Client) GetRecordA(ref string, opts *Options) (*RecordAObject, error) {
	resp, err := c.RecordAObject(ref).get(opts)
	if err != nil {
		return nil, fmt.Errorf("Could not get created A record: %s", err)
	}
	var out RecordAObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// FindRecordA searches the Infoblox WAPI for the A record with the given
// name
func (c *Client) FindRecordA(name string) ([]RecordAObject, error) {
	field := "name"
	conditions := []Condition{Condition{Field: &field, Value: name}}
	resp, err := c.RecordA().find(conditions, nil)
	if err != nil {
		return nil, err
	}

	var out []RecordAObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}
