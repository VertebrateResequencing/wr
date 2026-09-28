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

package jobqueue

// The manager holds its ports from the start of Serve, not from publication.
//
// While the manager's port is closed, a local client redialling it can be given
// that same port as its ephemeral source port, and TCP simultaneous open then
// connects the socket to itself. Go's dialer spots the self-connect and closes
// the socket, which leaves the port in TIME_WAIT for 60s. A dialled socket has
// no SO_REUSEADDR, so nothing, even with SO_REUSEADDR or SO_REUSEPORT, can bind
// the port until that expires. mangos redials every 100ms, and Linux hands out
// even source ports first, so an even port (the default web port, or any even
// configured port) is soon swept.
//
// A socket bound to the port, even one that is not listening, takes the port
// out of the kernel's choice of ephemeral source ports, and a client dialling
// it is refused just as if it were closed. So Serve binds one on each port with
// SO_REUSEADDR and holds it until publication's real listener, which also has
// SO_REUSEADDR, is bound alongside it.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
)

const (
	// serverBindLingerBudget is how long Serve retries a port that is held by
	// a socket nothing is listening on, such as a self-connect's TIME_WAIT.
	// It is longer than Linux's fixed 60s TIME_WAIT.
	serverBindLingerBudget = 90 * time.Second

	// serverBindLingerLogInterval is how often Serve says it is still waiting
	// for such a port.
	serverBindLingerLogInterval = 10 * time.Second

	// portListenerCheckTimeout bounds the dial Serve makes to learn whether a
	// port it cannot bind has a listener.
	portListenerCheckTimeout = time.Second
)

// errPortInUse and errPortStillHeld are what reservePort wraps its final bind
// error in.
var (
	errPortInUse     = errors.New("in use by another process")
	errPortStillHeld = errors.New("still held by a socket that is not listening (such as a connection in TIME_WAIT)")
)

// portReservation is a socket bound to a port without listening on it.
type portReservation struct {
	mu sync.Mutex
	fd int
}

// reservePort reserves port. If the port is in use, it retries every
// serverBindRetryInterval: for up to serverBindRetryBudget while something is
// listening on it (a server we recently stopped may not quite have finished),
// and for up to serverBindLingerBudget while nothing is (a TIME_WAIT that will
// expire).
//
// A port that cannot be reserved for any other reason, or that is not a
// specific port number, gets an empty reservation and no error: publication's
// own bind then reports any real problem, as it did before reservations.
func reservePort(ctx context.Context, what, port string) (*portReservation, error) {
	p, ok := reservablePort(port)
	if !ok {
		return unreserved(), nil
	}

	res, err := bindPortReservation(p)
	if !errors.Is(err, syscall.EADDRINUSE) {
		return reservationOutcome(ctx, what, port, res, err), nil
	}

	clog.Warn(ctx, "could not reserve the "+what+" yet, retrying", "port", port, "err", err)

	return retryPortReservation(ctx, what, port, p)
}

// unreserved returns an empty reservation, which holds no port.
func unreserved() *portReservation {
	return &portReservation{fd: -1}
}

// reservationOutcome returns res if the reservation succeeded, or else logs err
// and returns an empty reservation.
func reservationOutcome(ctx context.Context, what, port string, res *portReservation, err error) *portReservation {
	if err == nil {
		return res
	}

	clog.Warn(ctx, "could not reserve the "+what+"; it will be bound once recovery ends",
		"port", port, "err", err)

	return unreserved()
}

// retryPortReservation is reservePort's retry loop, entered once reserving port
// (p as a number) has failed with EADDRINUSE.
func retryPortReservation(ctx context.Context, what, port string, p int) (*portReservation, error) {
	started := time.Now()
	lastLog := started

	ticker := time.NewTicker(serverBindRetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}

		res, err := bindPortReservation(p)
		if !errors.Is(err, syscall.EADDRINUSE) {
			clog.Warn(ctx, "stopped waiting for the "+what, "port", port,
				"waited", time.Since(started).Round(time.Second), "reserved", err == nil)

			return reservationOutcome(ctx, what, port, res, err), nil
		}

		waited := time.Since(started)

		if errd := portHeldTooLong(ctx, port, waited, err); errd != nil {
			return nil, fmt.Errorf("%s %s is %w", what, port, errd)
		}

		lastLog = logPortStillHeld(ctx, what, port, waited, lastLog)
	}
}

// bindPortReservation binds a socket to 0.0.0.0:port with SO_REUSEADDR, but
// does not listen on it. SO_REUSEADDR lets it bind past the TIME_WAITs a
// previous manager's own connections leave on the port, and lets our real
// listener bind alongside it.
func bindPortReservation(port int) (*portReservation, error) {
	// hold ForkLock so that a process forked meanwhile cannot inherit the
	// socket before it is marked close-on-exec: a runner that did would hold
	// the port for as long as it lived.
	syscall.ForkLock.RLock()

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}

	syscall.ForkLock.RUnlock()

	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}

	if err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		_ = syscall.Close(fd)

		return nil, os.NewSyscallError("setsockopt", err)
	}

	if err = syscall.Bind(fd, &syscall.SockaddrInet4{Port: port}); err != nil {
		_ = syscall.Close(fd)

		return nil, os.NewSyscallError("bind", err)
	}

	return &portReservation{fd: fd}, nil
}

// release closes the reservation's socket. It may be called more than once,
// and on a nil reservation.
func (r *portReservation) release() {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.fd < 0 {
		return
	}

	_ = syscall.Close(r.fd)
	r.fd = -1
}

// serverPortReservations are the reservations Serve holds on the manager's
// ports until publication binds them for real.
type serverPortReservations struct {
	rpc *portReservation
	web *portReservation
}

// reserveServerPorts reserves config's manager and web ports. It fails if the
// manager port cannot be reserved, but only logs a web port it cannot reserve,
// since a manager whose web interface cannot bind still runs.
func reserveServerPorts(ctx context.Context, config ServerConfig) (*serverPortReservations, error) {
	rpc, err := reservePort(ctx, "manager port", config.Port)
	if err != nil {
		return nil, err
	}

	web, err := reservePort(ctx, "web interface port", config.WebPort)
	if err != nil {
		clog.Error(ctx, "could not reserve the web interface port, so the web interface may not start",
			"port", config.WebPort, "err", err)
	}

	return &serverPortReservations{rpc: rpc, web: web}, nil
}

// release releases both reservations. It may be called more than once, and on
// a nil *serverPortReservations (as on a hand-built test Server).
func (r *serverPortReservations) release() {
	if r == nil {
		return
	}

	r.rpc.release()
	r.web.release()
}

// rpcHandedOver releases the manager port's reservation, once publication's
// listener is bound alongside it.
func (r *serverPortReservations) rpcHandedOver() {
	if r != nil {
		r.rpc.release()
	}
}

// webHandedOver releases the web port's reservation, once publication has
// tried to bind its listener: with that bound, the reservation is not needed,
// and without, it is not worth keeping.
func (r *serverPortReservations) webHandedOver() {
	if r != nil {
		r.web.release()
	}
}

// reservablePort returns port as a number, and whether it is a specific port
// that can be reserved.
func reservablePort(port string) (int, bool) {
	p, err := strconv.Atoi(port)

	return p, err == nil && p > 0
}

// logPortStillHeld says the port is still held, if it has not been said since
// serverBindLingerLogInterval after lastLog, returning when it was last said.
func logPortStillHeld(ctx context.Context, what, port string, waited time.Duration, lastLog time.Time) time.Time {
	if time.Since(lastLog) < serverBindLingerLogInterval {
		return lastLog
	}

	clog.Warn(ctx, "the "+what+" is still in use; if nothing is listening on it, it is probably held by "+
		"a connection in TIME_WAIT, which will expire; still retrying", "port", port,
		"waited", waited.Round(time.Second), "budget", serverBindLingerBudget)

	return time.Now()
}

// portHeldTooLong returns an error wrapping bindErr if port has been in use for
// longer than it is worth waiting for, given whether something is listening on
// it.
func portHeldTooLong(ctx context.Context, port string, waited time.Duration, bindErr error) error {
	if localPortListening(ctx, port, portListenerCheckTimeout) {
		if waited >= serverBindRetryBudget {
			return fmt.Errorf("%w: %w", errPortInUse, bindErr)
		}

		return nil
	}

	if waited >= serverBindLingerBudget {
		return fmt.Errorf("%w after %s: %w", errPortStillHeld, waited.Round(time.Second), bindErr)
	}

	return nil
}

// localPortListening reports whether something on this host accepts
// connections on port, closing any connection it makes.
func localPortListening(ctx context.Context, port string, timeout time.Duration) bool {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer

	conn, err := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort("", port))
	if err != nil || conn == nil {
		return false
	}

	if errc := conn.Close(); errc != nil {
		clog.Warn(ctx, "closing a port check connection failed", "port", port, "err", errc)
	}

	return true
}
