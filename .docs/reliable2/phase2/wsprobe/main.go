package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

// delta mirrors the server's phase-2 status_ws message: a from->to state-count
// delta where the count in FromState drops by Count and the count in ToState
// rises by Count (v0.36.5-style).
type delta struct {
	RepGroup  string `json:"RepGroup"`
	FromState string `json:"FromState"`
	ToState   string `json:"ToState"`
	Count     int    `json:"Count"`
}

func main() {
	host := os.Args[1]  // host:webport
	token := os.Args[2] // web token
	secs := 3
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%d", &secs)
	}
	u := url.URL{Scheme: "wss", Host: host, Path: "/status_ws", RawQuery: "token=" + url.QueryEscape(token)}
	d := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 10 * time.Second}
	c, _, err := d.Dial(u.String(), nil)
	if err != nil {
		fmt.Println("DIAL ERROR:", err)
		os.Exit(1)
	}
	defer c.Close()
	// trigger the server's scan-on-connect seed, exactly as the web UI does on
	// open.
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"Request":"current"}`)); err != nil {
		fmt.Println("WRITE ERROR:", err)
		os.Exit(1)
	}
	// counts is the reconstructed per-RepGroup, per-state count, mirroring what
	// the web UI's status bars accumulate from the delta feed.
	counts := map[string]map[string]int{}
	received := false
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	c.SetReadDeadline(deadline)
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			break
		}
		var m delta
		if json.Unmarshal(data, &m) != nil || m.RepGroup == "" {
			continue
		}
		received = true
		rg := counts[m.RepGroup]
		if rg == nil {
			rg = map[string]int{}
			counts[m.RepGroup] = rg
		}
		// the seed uses FromState "new" as a source with no bar of its own; skip
		// it (the web UI does the same).
		if m.FromState != "" && m.FromState != "new" {
			rg[m.FromState] -= m.Count
			if rg[m.FromState] <= 0 { // clamp at 0, as the web UI does
				delete(rg, m.FromState)
			}
		}
		if m.ToState != "" && m.ToState != "new" {
			rg[m.ToState] += m.Count
			if rg[m.ToState] <= 0 { // clamp at 0, as the web UI does
				delete(rg, m.ToState)
			}
		}
	}
	// print what the web UI would show (non-zero state counts per RepGroup).
	for rg, sc := range counts {
		fmt.Printf("WEBUI rg=%q counts=%v\n", rg, sc)
	}
	if !received {
		fmt.Println("WEBUI (no state messages received)")
	}
}
