package infoblox

// Ipv4address returns an Infoblox Ipv4 address resource
// https://192.168.2.200/wapidoc/objects/ipv4address.html
func (c *Client) Ipv4address() *Resource {
	return &Resource{
		conn:       c,
		wapiObject: "ipv4address",
	}
}

// Ipv4addressObject defines the Ipv4 address object's fields
type Ipv4addressObject struct {
	Object
	DHCPClientIdentifier string   `json:"dhcp_client_identifier,omitempty"`
	IPAddress            string   `json:"ip_address,omitempty"`
	IsConflict           bool     `json:"is_conflict"`
	LeaseState           string   `json:"lease_state,omitempty"`
	MACAddress           string   `json:"mac_address,omitempty"`
	Names                []string `json:"names,omitempty"`
	Network              string   `json:"network,omitempty"`
	NetworkView          string   `json:"network_view,omitempty"`
	Objects              []string `json:"objects,omitempty"`
	Status               string   `json:"status,omitempty"`
	Types                []string `json:"types,omitempty"`
	Usage                []string `json:"usage,omitempty"`
	Username             string   `json:"username,omitempty"`
}

// Ipv4addressObject instantiates an Ipv4 address object with a WAPI ref
func (c *Client) Ipv4addressObject(ref string) *Ipv4addressObject {
	ip := Ipv4addressObject{}
	ip.Object = Object{
		Ref: ref,
		r:   c.Ipv4address(),
	}
	return &ip
}

// FindIP searches the Infoblox WAPI for the Ipv4address object with the given
// IP address
func (c *Client) FindIP(ip string) ([]Ipv4addressObject, error) {
	field := "ip_address"
	conditions := []Condition{Condition{Field: &field, Value: ip}}
	resp, err := c.Ipv4address().find(conditions, nil)
	if err != nil {
		return nil, err
	}

	var out []Ipv4addressObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FindUnusedIPInRange searches the Infoblox WAPI for unused IP addresses in the
// given IP range.
func (c *Client) FindUnusedIPInRange(start string, end string) ([]Ipv4addressObject, error) {
	ipField := "ip_address"
	statusField := "status"
	conditions := []Condition{
		Condition{Field: &ipField, Value: start, Modifiers: ">"},
		Condition{Field: &ipField, Value: end, Modifiers: "<"},
		Condition{Field: &statusField, Value: "UNUSED"},
	}
	resp, err := c.Ipv4address().find(conditions, nil)
	if err != nil {
		return nil, err
	}

	var out []Ipv4addressObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}
