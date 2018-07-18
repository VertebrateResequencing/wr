package main

import (
	"fmt"
	"net/url"

	infoblox "github.com/fanatic/go-infoblox"
)

func main() {
	// URL, username, password, sslVerify, useCookies
	ib := infoblox.NewClient("https://192.168.2.200/", "admin", "infoblox", false, false)

	// All networks
	printList(ib.Network().All(nil))

	// All network with options
	maxResults := 1000
	opts := &infoblox.Options{
		MaxResults:   &maxResults,
		ReturnFields: []string{"network"},
	}
	printList(ib.Network().All(opts))

	// Find specific network
	s := "network"
	q := []infoblox.Condition{
		infoblox.Condition{
			Field: &s,
			Value: "164.55.90.0/24",
		},
	}
	out, err := ib.Network().Find(q, nil)
	printList(out, err)

	// Find info about an IP address
	s = "ip_address"
	q = []infoblox.Condition{
		infoblox.Condition{
			Field: &s,
			Value: "192.168.11.10",
		},
	}
	out, err = ib.Ipv4address().Find(q, nil)
	printList(out, err)

	// Example function call
	printObject(ib.NetworkObject(out[0]["_ref"].(string)).NextAvailableIP(5, nil))

	// Create a network
	d := url.Values{}
	d.Set("network", "192.168.11.0/24")
	d.Set("comment", "created example")
	ref, err := ib.Network().Create(d, nil, nil)
	printString(ref, err)

	if ref != "" {
		// Update it
		d = url.Values{}
		d.Set("comment", "updated example")
		printString(ib.NetworkObject(ref).Update(d, nil, nil))

		// Get it
		printObject(ib.NetworkObject(ref).Get(nil))

		// Delete it
		printError(ib.NetworkObject(ref).Delete(nil))
	}
}

func printList(out []map[string]interface{}, err error) {
	e(err)
	for i, v := range out {
		fmt.Printf("[%d]\n", i)
		printObject(v, nil)
	}
}

func printObject(out map[string]interface{}, err error) {
	e(err)
	for k, v := range out {
		fmt.Printf("  %s: %q\n", k, v)
	}
	fmt.Printf("\n")
}

func printString(out string, err error) {
	e(err)
	fmt.Printf("  %q\n\n", out)
}

func printError(err error) {
	e(err)
	fmt.Printf("SUCCESS\n")
}

func e(err error) {
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}
