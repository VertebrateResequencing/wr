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

// jstateAbsolute mirrors the server's status_ws message.
type msg struct {
	RepGroup string         `json:"RepGroup"`
	Counts   map[string]int `json:"Counts"`
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
	latest := map[string]map[string]int{}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	c.SetReadDeadline(deadline)
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			break
		}
		var m msg
		if json.Unmarshal(data, &m) == nil && m.Counts != nil {
			latest[m.RepGroup] = m.Counts
		}
	}
	// print what the web UI would show
	for rg, counts := range latest {
		fmt.Printf("WEBUI rg=%q counts=%v\n", rg, counts)
	}
	if len(latest) == 0 {
		fmt.Println("WEBUI (no state messages received)")
	}
}
