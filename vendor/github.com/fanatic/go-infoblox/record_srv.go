package infoblox

import "fmt"

// RecordSrv returns the SRV record resource
func (c *Client) RecordSrv() *Resource {
	return &Resource{
		conn:       c,
		wapiObject: "record:srv",
	}
}

// RecordSrvObject defines the SRV record object's fields
type RecordSrvObject struct {
	Object
	Comment  string `json:"comment,omitempty"`
	Name     string `json:"name,omitempty"`
	Target   string `json:"target,omitempty"`
	Port     int    `json:"port,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Weight   int    `json:"weight,omitempty"`
	Pref     int    `json:"pref,omitempty"`
	Ttl      int    `json:"ttl,omitempty"`
	View     string `json:"view,omitempty"`
}

// RecordSrvObject instantiates an SRV record object with a WAPI ref
func (c *Client) RecordSrvObject(ref string) *RecordSrvObject {
	a := RecordSrvObject{}
	a.Object = Object{
		Ref: ref,
		r:   c.RecordSrv(),
	}
	return &a
}

// GetRecordSrv fetches an SRV record from the Infoblox WAPI by its ref
func (c *Client) GetRecordSrv(ref string, opts *Options) (*RecordSrvObject, error) {
	resp, err := c.RecordSrvObject(ref).get(opts)
	if err != nil {
		return nil, fmt.Errorf("Could not get created SRV record: %s", err)
	}
	var out RecordSrvObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
