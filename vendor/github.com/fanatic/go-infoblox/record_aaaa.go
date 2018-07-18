package infoblox

import "fmt"

// RecordAAAA returns the AAAA record resource
// https://102.168.2.200/wapidoc/objects/record.aaaa.html
func (c *Client) RecordAAAA() *Resource {
	return &Resource{
		conn:       c,
		wapiObject: "record:aaaa",
	}
}

// RecordAAAAObject defines the AAAA record object's fields
type RecordAAAAObject struct {
	Object
	Comment  string `json:"comment,omitempty"`
	Ipv6Addr string `json:"ipv6addr,omitempty"`
	Name     string `json:"name,omitempty"`
	Ttl      int    `json:"ttl,omitempty"`
	View     string `json:"view,omitempty"`
}

// RecordAAAAObject instantiates an AAAA record object with a WAPI ref
func (c *Client) RecordAAAAObject(ref string) *RecordAAAAObject {
	a := RecordAAAAObject{}
	a.Object = Object{
		Ref: ref,
		r:   c.RecordAAAA(),
	}
	return &a
}

// GetRecordAAAA fetches an AAAA record from the Infoblox WAPI by its ref
func (c *Client) GetRecordAAAA(ref string, opts *Options) (*RecordAAAAObject, error) {
	resp, err := c.RecordAAAAObject(ref).get(opts)
	if err != nil {
		return nil, fmt.Errorf("Could not get created AAAA record: %s", err)
	}
	var out RecordAAAAObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// FindRecordAAAA searches the Infoblox WAPI for the AAAA record with the given
// name
func (c *Client) FindRecordAAAA(name string) ([]RecordAAAAObject, error) {
	field := "name"
	conditions := []Condition{Condition{Field: &field, Value: name}}
	resp, err := c.RecordAAAA().find(conditions, nil)
	if err != nil {
		return nil, err
	}

	var out []RecordAAAAObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}
