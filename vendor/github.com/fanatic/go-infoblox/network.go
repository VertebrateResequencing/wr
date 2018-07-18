package infoblox

import "fmt"

// Network returns an Infoblox Network resource
// https://192.168.2.200/wapidoc/objects/network.html
func (c *Client) Network() *Resource {
	return &Resource{
		conn:       c,
		wapiObject: "network",
	}
}

// NetworkObject defines the Infoblox Network object's fields
type NetworkObject struct {
	Object
	Comment     string  `json:"comment,omitempty"`
	Network     string  `json:"network,omitempty"`
	NetworkView string  `json:"network_view,omitempty"`
	Netmask     int     `json:"netmask,omitempty"`
	ExtAttrs    ExtAttr `json:"extattrs,omitempty"`
}

// ExtAttr is a map that contains extensible Infoblox attributes
type ExtAttr map[string]struct {
	Value interface{} `json:"value"`
}

// Get gets a string value from ExtAttrs
func (e ExtAttr) Get(key string) (string, bool) {
	v, ok := e[key]
	if !ok {
		return "", false
	}
	o, ok := v.Value.(string)
	return o, ok
}

// GetFloat gets a float value from ExtAttrs
func (e ExtAttr) GetFloat(key string) (float64, bool) {
	v, ok := e[key]
	if !ok {
		return -1, false
	}
	o, ok := v.Value.(float64)
	return o, ok
}

// NetworkObject instantiates a Network object with a WAPI ref
func (c *Client) NetworkObject(ref string) *NetworkObject {
	obj := NetworkObject{}
	obj.Object = Object{
		Ref: ref,
		r:   c.Network(),
	}
	return &obj
}

// NextAvailableIPParams defines the paramters that can be passed to the
// next_available_ip Infoblox WAPI function call.
// You may optionally specify how many IPs you want (num) and which ones to
// exclude from consideration (array of IPv4 addrdess strings).
type NextAvailableIPParams struct {
	Exclude []string `json:"exclude,omitempty"`
	Num     int      `json:"num,omitempty"`
}

// NextAvailableIP invokes the same-named function on the network resource
// in WAPI, returning an array of available IP addresses.
func (n NetworkObject) NextAvailableIP(num int, exclude []string) (map[string]interface{}, error) {
	if num == 0 {
		num = 1
	}

	v := &NextAvailableIPParams{
		Exclude: exclude,
		Num:     num,
	}

	out, err := n.FunctionCall("next_available_ip", v)
	if err != nil {
		return nil, fmt.Errorf("Error sending request: %v", err)
	}
	return out, nil
}

// FindNetworkByNetwork searches the Infoblox WAPI for a network by its network
// field
func (c *Client) FindNetworkByNetwork(net string) ([]NetworkObject, error) {
	field := "network"
	o := Options{ReturnFields: []string{"extattrs", "netmask"}}
	conditions := []Condition{Condition{Field: &field, Value: net}}
	resp, err := c.Network().find(conditions, &o)
	if err != nil {
		return nil, err
	}

	var out []NetworkObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FindNetworkByExtAttrs searches the Infoblox WAPI for a network by its extra
// attrs field
func (c *Client) FindNetworkByExtAttrs(attrs map[string]string) ([]NetworkObject, error) {
	conditions := []Condition{}
	for k, v := range attrs {
		attr := k
		conditions = append(conditions, Condition{Attribute: &attr, Value: v})
	}
	o := Options{ReturnFields: []string{"extattrs", "netmask"}, ReturnBasicFields: true}
	resp, err := c.Network().find(conditions, &o)
	if err != nil {
		return nil, err
	}

	var out []NetworkObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}
