// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of l15h.
//
//  l15h is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  l15h is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with l15h. If not, see <http://www.gnu.org/licenses/>.

/*
Package l15h provides some useful Handlers for use with log15:
https://github.com/inconshreveable/log15.

# Usage

    import (
        log "github.com/inconshreveable/log15"
        "github.com/sb10/l15h"
    )

    // Store logs in memory for dealing with later
    store := l15h.NewStore()
    h := log.MultiHandler(
        l15h.StoreHandler(store, log.LogfmtFormat()),
        log.StderrHandler,
    )
    log.Root().SetHandler(h)
    log.Debug("debug")
    log.Info("info")
    logs := store.Logs() // logs is a slice of your 2 log messages as strings

    // Always annotate your logs (other than Info()) with some caller
    // information, with Crit() getting a stack trace
    h = l15h.CallerInfoHandler(log.StderrHandler)
    log.Root().SetHandler(h)
    log.Debug("debug") // includes a caller=
    log.Info("info") // nothing added
    log.Crit("crit") // includes a stack=

    // Combine the above together
    h = l15h.CallerInfoHandler(
        log.MultiHandler(
            l15h.StoreHandler(store, log.LogfmtFormat()),
            log.StderrHandler,
        )
    )
    //...

    // Have child loggers that change how they log when their parent's Handler
    // changes
    changer := l15h.NewChanger(log15.DiscardHandler())
    log.Root().SetHandler(l15h.ChangeableHandler(changer))
    log.Info("discarded") // nothing logged

    childLogger := log.New("child", "context")
    store = l15h.NewStore()
    l15h.AddHandler(childLogger, l15h.StoreHandler(store, log.LogfmtFormat()))

    childLogger.Info("one") // len(store.Logs()) == 1

    changer.SetHandler(log15.StderrHandler)
    log.Info("logged") // logged to STDERR
    childLogger.Info("two") // logged to STDERR and len(store.Logs()) == 2

    // We have Panic and Fatal methods
    l15h.Panic("msg")
    l15h.Fatal("msg")
*/
package l15h

import (
	"fmt"
	"github.com/go-stack/stack"
	"github.com/inconshreveable/log15"
	"os"
	"sync"
)

var exitFunc = os.Exit

// Store struct is used for passing to StoreHandler. Create one, pass it to
// StoreHandler and set that handler on your logger, then log messages.
// Store.Logs() will then give you access to everything that was logged.
type Store struct {
	mutex    sync.RWMutex
	storeage []string
}

// NewStore creates a Store for supplying to StoreHandler and keeping for later
// use.
func NewStore() *Store {
	return &Store{}
}

// keep stores a new log in our storage.
func (s *Store) keep(log string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.storeage = append(s.storeage, log)
}

// Logs returns all the log messages that have been logged with this Store.
func (s *Store) Logs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.storeage[:]
}

// Clear empties this Store's storage. Afterwards, Logs() will return an empty
// slice.
func (s *Store) Clear() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.storeage = nil
}

// StoreHandler stores log records in the given Store. It is safe to perform
// concurrent stores. StoreHandler wraps itself with LazyHandler to evaluate
// Lazy objects.
func StoreHandler(store *Store, fmtr log15.Format) log15.Handler {
	h := log15.FuncHandler(func(r *log15.Record) error {
		store.keep(string(fmtr.Format(r)))
		return nil
	})
	return log15.LazyHandler(h)
}

// CallerInfoHandler returns a Handler that, at the Debug, Warn and Error
// levels, adds the file and line number of the calling function to the context
// with key "caller". At the Crit levels it instead adds a stack trace to the
// context with key "stack". The stack trace is formatted as a space separated
// list of call sites inside matching []'s. The most recent call site is listed
// first.
func CallerInfoHandler(h log15.Handler) log15.Handler {
	return log15.FuncHandler(func(r *log15.Record) error {
		switch r.Lvl {
		case log15.LvlDebug, log15.LvlWarn, log15.LvlError:
			r.Ctx = append(r.Ctx, "caller", fmt.Sprint(r.Call))
		case log15.LvlCrit:
			s := stack.Trace().TrimBelow(r.Call).TrimRuntime()
			if len(s) > 0 {
				r.Ctx = append(r.Ctx, "stack", fmt.Sprintf("%+v", s))
			}
		}
		return h.Log(r)
	})
}

// Changer struct lets you dynamically change the Handler of all loggers that
// inherit from a logger that uses a ChangeableHandler, which takes one of
// these. A Changer is safe for concurrent use.
type Changer struct {
	mutex   sync.RWMutex
	handler log15.Handler
}

// NewChanger creates a Changer for supplying to ChangeableHandler and keeping
// for later use.
func NewChanger(h log15.Handler) *Changer {
	return &Changer{handler: h}
}

// GetHandler returns the previously set Handler.
func (c *Changer) GetHandler() log15.Handler {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.handler
}

// SetHandler sets a new Handler on the Changer, so that any logger that is
// using a ChangeableHandler with this Changer will now log to this new Handler.
func (c *Changer) SetHandler(h log15.Handler) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.handler = h
}

// ChangeableHandler is for use when you your library will start with a base
// logger and create New() loggers from that, inheriting its Handler and
// possibly adding its own (eg. with AddHandler()) such as the StoreHandler.
// But you want a user of your library to be able to change the Handler for all
// your loggers. You can do this by setting a ChangeableHandler on your base
// logger, and giving users of your library access to your Changer, which they
// can call SetHandler() on to define how things are logged any way they like.
func ChangeableHandler(changer *Changer) log15.Handler {
	return log15.FuncHandler(func(r *log15.Record) error {
		return changer.GetHandler().Log(r)
	})
}

// AddHandler is a convenience for adding an additional handler to a logger,
// keeping any existing ones. Note that this simply puts the existing handler
// and the new handler within a MultiHandler, so you should probably avoid
// calling this too many times on the same logger (or its descendants).
func AddHandler(l log15.Logger, h log15.Handler) {
	l.SetHandler(log15.MultiHandler(
		l.GetHandler(),
		h,
	))
}

// Panic logs a message at the Crit level with key/val panic=true added to the
// context and then calls panic(msg). Operates on the root Logger. The panic
// will send a complete stack trace to STDERR and cause the application to
// terminate after calling deferred functions unless recovered.
func Panic(msg string, ctx ...interface{}) {
	PanicContext(log15.Root(), msg, ctx...)
}

// PanicContext is like Panic(), but also takes a Logger if you want to log to
// something other than root.
func PanicContext(l log15.Logger, msg string, ctx ...interface{}) {
	ctx = append(ctx, "panic", true)
	l.Crit(msg, ctx...)
	panic(msg)
}

// Fatal logs a message at the Crit level with key/val fatal=true added to the
// context and then calls os.Exit(1). Operates on the root Logger. The exit is
// not recoverable and deferred functions do not get called.
func Fatal(msg string, ctx ...interface{}) {
	FatalContext(log15.Root(), msg, ctx...)
}

// FatalContext is like Fatal(), but also takes a Logger if you want to log to
// something other than root.
func FatalContext(l log15.Logger, msg string, ctx ...interface{}) {
	ctx = append(ctx, "fatal", true)
	l.Crit(msg, ctx...)
	exitFunc(1)
}

// SetExitFunc should typically only be used if you're testing a place where
// your code should call Fatal() or FatalContext() but don't want your test
// script to actually exit. Your supplied function will be called at the end of
// those methods instead of os.Exit(1).
func SetExitFunc(ef func(code int)) {
	exitFunc = ef
}
