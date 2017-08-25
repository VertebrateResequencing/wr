// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package rp

// This file contains the implementation of request and Receipt.

import (
	"sync"
	"time"
)

// Receipt is the unique id of a request
type Receipt string

// request struct describes a request for tokens tied to a particular resource
// protector.
type request struct {
	id          Receipt
	owner       string
	numTokens   int
	grantedCh   chan bool
	cancelCh    chan bool
	releaseCh   chan bool
	touchCh     chan bool
	autoRelease time.Duration
	waiting     bool
	active      bool
	done        bool
	mu          sync.RWMutex
}

// waitUntilGranted blocks until the Protector that created us sends on our
// grantedCh. Returns false if finished(), or if cancelled by the Protector
// sending on our cancelCh, or if another caller is waiting on this method.
// Returns true if granted while calling this method, if if it had been
// previously granted and the grant is still valid.
func (r *request) waitUntilGranted() bool {
	r.mu.Lock()
	if r.done || r.waiting {
		r.mu.Unlock()
		return false
	} else if r.active {
		r.mu.Unlock()
		return true
	}
	r.waiting = true
	r.mu.Unlock()
	select {
	case <-r.grantedCh:
		return true
	case <-r.cancelCh:
		r.mu.Lock()
		r.done = true
		r.waiting = false
		r.mu.Unlock()
		return false
	}
	return false
}

// touch sends on our touchCh, which will be read by the Protector that granted
// our tokens to stop it timing out and auto-releasing.
func (r *request) touch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.done {
		return
	}
	r.touchCh <- true
}

// release sends on our releaseCh, which will be read by the Protector that
// granted our tokens. Finally does the equivalent of finish().
func (r *request) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.done {
		return
	}
	r.done = true
	r.releaseCh <- true
}

// grant is called to signify a request was granted.
func (r *request) grant() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = true
	if r.waiting {
		r.waiting = false
		r.grantedCh <- true
	}
}

// finish stops new calls to waitUntilGranted(), touch() and release() from
// doing anything (but does not cancel an ongoing waitUntilGranted()).
func (r *request) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
}

// granted tells you if the request has been granted via a successful call to
// waitUntilGranted() and is not yet finished().
func (r *request) granted() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active && !r.done
}

// finished tells you if the request has been cancelled or released.
func (r *request) finished() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.done
}
