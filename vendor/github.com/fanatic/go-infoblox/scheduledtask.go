package infoblox

// ScheduledTask returns an Infoblox Scheduled Task resource
// https://192.168.2.200/wapidoc/objects/scheduledtask.html
func (c *Client) ScheduledTask() *Resource {
	return &Resource{
		conn:       c,
		wapiObject: "scheduledtask",
	}
}

// ScheduledTaskObject defines the Infoblox Scheduled Task object's fields
type ScheduledTaskObject struct {
	Object
}

// ScheduledTaskObject instantiates a ScheduledTask object with a WAPI ref
func (c *Client) ScheduledTaskObject(ref string) *ScheduledTaskObject {
	return &ScheduledTaskObject{
		Object{
			Ref: ref,
			r:   c.ScheduledTask(),
		},
	}
}

// FindScheduledTask  searches the Infoblox WAPI for Scheduled Tasks that have
// the given name
func (c *Client) FindScheduledTask(name string) ([]ScheduledTaskObject, error) {
	field := "changed_objects.name"
	o := Options{ReturnFields: []string{"changed_objects"}, ReturnBasicFields: true}
	conditions := []Condition{Condition{Field: &field, Value: name}}
	resp, err := c.ScheduledTask().find(conditions, &o)
	if err != nil {
		return nil, err
	}

	var out []ScheduledTaskObject
	err = resp.Parse(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}
