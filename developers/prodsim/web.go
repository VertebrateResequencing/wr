/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsHandshakeTimeout = 30 * time.Second
	wsReadTimeout      = 10 * time.Minute
	wsWriteTimeout     = 30 * time.Second
	wsRedialPause      = time.Second
	wsMinStayMins      = 5
	wsStaySpreadMins   = 55
	wsClickEvery       = 3 * time.Minute
	wsSearchChance     = 0.05
	allRepGroups       = "+all+"
	wsRequest          = "Request"
	wsRepGroup         = "RepGroup"
)

// wsMsg is the part of a status page websocket message the users look at.
type wsMsg struct {
	SeedBoundary string
	RepGroup     string
	Key          string
}

// webUsers runs cfg.webClients status page users.
func (s *sim) webUsers(ctx context.Context) {
	if s.cfg.webAddr == "" {
		return
	}

	var wg sync.WaitGroup

	for i := range s.cfg.webClients {
		wg.Go(func() { s.webUser(ctx, fmt.Sprintf("web%d", i)) })
	}

	wg.Wait()
}

// webUser behaves like one person with the status page open: it connects,
// takes the seed, keeps reading the live feed, and every so often refreshes
// the page, clicks on a rep group, or searches.
func (s *sim) webUser(ctx context.Context, actor string) {
	for ctx.Err() == nil {
		conn := retryConnect(ctx, s, actor, "ws_dial", func() (*websocket.Conn, error) { return s.wsDial(ctx) })
		if conn == nil {
			return
		}

		s.webSession(ctx, actor, conn)

		if err := conn.Close(); err != nil {
			s.event(actor, "close: "+err.Error())
		}

		if !sleep(ctx, wsRedialPause) {
			return
		}
	}
}

// wsDial opens the status page's websocket, re-reading the token each time as
// the page does when it is reloaded.
func (s *sim) wsDial(ctx context.Context) (*websocket.Conn, error) {
	token, err := os.ReadFile(filepath.Join(s.cfg.runDir, "client.token"))
	if err != nil {
		return nil, err
	}

	u := url.URL{Scheme: "wss", Host: s.cfg.webAddr, Path: "/status_ws",
		RawQuery: "token=" + url.QueryEscape(strings.TrimSpace(string(token)))}
	d := websocket.Dialer{
		// the isolated manager's self-signed cert
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		HandshakeTimeout: wsHandshakeTimeout,
	}

	conn, resp, err := d.DialContext(ctx, u.String(), nil)
	if resp != nil {
		// a failed body close must not turn a working connection into a
		// failure, which the caller would then leak
		if errb := resp.Body.Close(); errb != nil && err != nil {
			err = errors.Join(err, errb)
		}
	}

	return conn, err
}

// wsSession is what one page view has seen so far.
type wsSession struct {
	s     *sim
	actor string

	mu         sync.Mutex
	seedStart  time.Time
	seedMsgs   int
	inSeed     bool
	rgs        map[string]bool
	detailSent time.Time
	detailMsgs int
	deltas     int
}

// read handles messages from conn until it fails.
func (w *wsSession) read(conn *websocket.Conn) {
	for {
		if err := conn.SetReadDeadline(time.Now().Add(wsReadTimeout)); err != nil {
			return
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var m wsMsg
		if json.Unmarshal(data, &m) == nil {
			w.handle(m)
		}
	}
}

func (w *wsSession) handle(m wsMsg) {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch {
	case m.SeedBoundary == "begin":
		w.inSeed = true
	case m.SeedBoundary == "end":
		w.inSeed = false
		w.s.callLine(w.actor, "ws_seed", time.Since(w.seedStart).Milliseconds(), w.seedMsgs, "")
	case m.Key != "":
		w.detailMsgs++
		if w.detailMsgs == 1 && !w.detailSent.IsZero() {
			w.s.callLine(w.actor, "ws_details_first", time.Since(w.detailSent).Milliseconds(), 0, "")
		}
	case m.RepGroup != "":
		w.countRepGroup(m.RepGroup)
	}
}

// countRepGroup counts a rep group count message, as part of the seed or as a
// live delta, and remembers the group for clicking on. Callers hold mu.
func (w *wsSession) countRepGroup(rg string) {
	if w.inSeed {
		w.seedMsgs++
	} else {
		w.deltas++
	}

	if rg != allRepGroups {
		w.rgs[rg] = true
	}
}

// pickRepGroup returns a random rep group the page has shown, and starts
// timing the details request about to be sent for it.
func (w *wsSession) pickRepGroup() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.rgs) == 0 {
		return "", false
	}

	names := make([]string, 0, len(w.rgs))
	for rg := range w.rgs {
		names = append(names, rg)
	}

	w.detailSent = time.Now()
	w.detailMsgs = 0

	return names[w.s.intn(len(names))], true
}

// webSession is one page view: it asks for the seed, then until the page is
// "refreshed" (simulated 5-60 minutes) clicks on a rep group now and then.
func (s *sim) webSession(ctx context.Context, actor string, conn *websocket.Conn) {
	w := &wsSession{s: s, actor: actor, rgs: map[string]bool{}, seedStart: time.Now()}
	done := make(chan struct{})

	go func() {
		defer close(done)

		w.read(conn)
	}()

	send := func(req map[string]any) bool { return wsSend(conn, req) }

	if !send(map[string]any{wsRequest: "current"}) {
		return
	}

	deadline := time.Now().Add(s.sim(time.Duration(wsMinStayMins+s.intn(wsStaySpreadMins)) * time.Minute))

	for time.Now().Before(deadline) && s.webWait(ctx, done, s.jitter(s.sim(wsClickEvery))) {
		if rg, ok := w.pickRepGroup(); ok && !s.webClick(ctx, actor, rg, send) {
			break
		}
	}

	w.mu.Lock()
	s.callLine(actor, "ws_session_deltas", 0, w.deltas, "")
	w.mu.Unlock()
}

// wsSend sends req, reporting whether it could.
func wsSend(conn *websocket.Conn, req map[string]any) bool {
	if err := conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout)); err != nil {
		return false
	}

	return conn.WriteJSON(req) == nil
}

// webWait waits d, reporting false if the connection or the run ends first.
func (s *sim) webWait(ctx context.Context, done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return false
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// webClick asks for rg's details as a click does (or, now and then, searches
// for its first word as the search box does, with no limit), looks for a
// simulated minute, then unsubscribes. It reports false when the run ends.
func (s *sim) webClick(ctx context.Context, actor, rg string, send func(map[string]any) bool) bool {
	if s.float() < wsSearchChance {
		word, _, _ := strings.Cut(rg, "_")
		send(map[string]any{wsRequest: "details", wsRepGroup: word, "Search": true})
		s.event(actor, "search "+word)
	} else {
		states := []string{"complete", "running", "ready", "buried", "dependent"}
		send(map[string]any{wsRequest: "details", wsRepGroup: rg, "State": states[s.intn(len(states))], "Limit": 1})
	}

	if !sleep(ctx, s.sim(time.Minute)) {
		return false
	}

	send(map[string]any{wsRequest: "unsubscribe"})

	return true
}
