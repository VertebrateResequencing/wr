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

// Copyright 2019 The Mangos Authors
// Copyright 2026 Genome Research Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package mangostlstcp registers a race-clean TLS over TCP transport for
// mangos. It is based on go.nanomsg.org/mangos/v3/transport/tlstcp.
package mangostlstcp

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/transport"
)

// Transport is a transport.Transport for TLS over TCP.
const Transport = tlsTran(0)

func init() { //nolint:gochecknoinits // Mangos transports are discovered through package registration.
	transport.RegisterTransport(Transport)
}

type connPipe struct {
	c       net.Conn
	proto   transport.ProtocolInfo
	closed  bool
	options map[string]interface{}
	maxrx   int
	sync.Mutex
}

func newConnPipe(c net.Conn, proto transport.ProtocolInfo) *connPipe {
	p := &connPipe{
		c:       c,
		proto:   proto,
		options: make(map[string]interface{}),
	}

	p.options[mangos.OptionMaxRecvSize] = 0
	p.options[mangos.OptionLocalAddr] = c.LocalAddr()
	p.options[mangos.OptionRemoteAddr] = c.RemoteAddr()

	return p
}

func (p *connPipe) Recv() (*transport.Message, error) {
	var sz int64
	if err := binary.Read(p.c, binary.BigEndian, &sz); err != nil {
		return nil, err
	}

	p.Lock()
	maxrx := p.maxrx
	p.Unlock()

	if sz < 0 || (maxrx > 0 && sz > int64(maxrx)) {
		return nil, mangos.ErrTooLong
	}

	msg := mangos.NewMessage(int(sz))

	msg.Body = msg.Body[0:sz]
	if _, err := io.ReadFull(p.c, msg.Body); err != nil {
		msg.Free()

		return nil, err
	}

	return msg, nil
}

func (p *connPipe) Send(msg *transport.Message) error {
	l := uint64(len(msg.Header) + len(msg.Body))
	lbyte := make([]byte, 8)
	binary.BigEndian.PutUint64(lbyte, l)

	buff := net.Buffers{lbyte, msg.Header, msg.Body}
	if _, err := buff.WriteTo(p.c); err != nil {
		return err
	}

	msg.Free()

	return nil
}

func (p *connPipe) Close() error {
	p.Lock()
	if p.closed {
		p.Unlock()

		return nil
	}

	p.closed = true
	conn := p.c
	p.Unlock()

	return conn.Close()
}

func (p *connPipe) GetOption(n string) (interface{}, error) {
	p.Lock()
	defer p.Unlock()

	if n == mangos.OptionMaxRecvSize {
		return p.maxrx, nil
	}

	if v, ok := p.options[n]; ok {
		return v, nil
	}

	return nil, mangos.ErrBadProperty
}

func (p *connPipe) SetOption(n string, v interface{}) {
	p.Lock()
	defer p.Unlock()

	if n == mangos.OptionMaxRecvSize {
		if maxrx, ok := v.(int); ok {
			p.maxrx = maxrx
		}
	}

	p.options[n] = v
}

type connHeader struct {
	Zero     byte
	S        byte
	P        byte
	Version  byte
	Proto    uint16
	Reserved uint16
}

func (p *connPipe) handshake() error {
	h := connHeader{S: 'S', P: 'P', Proto: p.proto.Self}
	if err := binary.Write(p.c, binary.BigEndian, &h); err != nil {
		return err
	}

	if err := binary.Read(p.c, binary.BigEndian, &h); err != nil {
		_ = p.Close()

		return err
	}

	if err := validateHeader(h, p.proto.Peer); err != nil {
		_ = p.Close()

		return err
	}

	p.Lock()
	defer p.Unlock()

	if p.closed {
		return mangos.ErrClosed
	}

	return nil
}

func validateHeader(h connHeader, peer uint16) error {
	if h.Zero != 0 || h.S != 'S' || h.P != 'P' || h.Reserved != 0 {
		return mangos.ErrBadHeader
	}

	if h.Version != 0 {
		return mangos.ErrBadVersion
	}

	if h.Proto != peer {
		return mangos.ErrBadProto
	}

	return nil
}

type handshakerPipe interface {
	handshake() error
	transport.Pipe
}

type handshakerItem struct {
	c handshakerPipe
	e error
}

type handshaker struct {
	workq  map[handshakerPipe]bool
	doneq  []*handshakerItem
	closed bool
	cv     *sync.Cond
	wg     sync.WaitGroup
	sync.Mutex
}

func newHandshaker() *handshaker {
	h := &handshaker{
		workq: make(map[handshakerPipe]bool),
	}
	h.cv = sync.NewCond(h)

	return h
}

func (h *handshaker) Wait() (transport.Pipe, error) {
	h.Lock()
	defer h.Unlock()

	for len(h.doneq) == 0 && !h.closed {
		h.cv.Wait()
	}

	if h.closed {
		return nil, mangos.ErrClosed
	}

	item := h.doneq[0]
	h.doneq = h.doneq[1:]

	return item.c, item.e
}

func (h *handshaker) Start(p transport.Pipe) {
	conn, ok := p.(handshakerPipe)
	if !ok {
		_ = p.Close()

		return
	}

	h.Lock()
	if h.closed {
		h.Unlock()

		_ = conn.Close()

		return
	}

	h.workq[conn] = true
	h.wg.Add(1)
	h.Unlock()

	go h.worker(conn)
}

func (h *handshaker) Close() {
	h.Lock()
	if h.closed {
		h.Unlock()
		h.wg.Wait()

		return
	}

	h.closed = true
	h.cv.Broadcast()

	work := make([]handshakerPipe, 0, len(h.workq))
	for conn := range h.workq {
		work = append(work, conn)
	}

	done := h.doneq
	h.doneq = nil
	h.Unlock()

	for _, conn := range work {
		_ = conn.Close()
	}

	for _, item := range done {
		if item.c != nil {
			_ = item.c.Close()
		}
	}

	h.wg.Wait()
}

func (h *handshaker) worker(conn handshakerPipe) {
	defer h.wg.Done()

	item := &handshakerItem{c: conn}
	item.e = conn.handshake()

	h.Lock()
	defer h.Unlock()

	delete(h.workq, conn)

	if item.e != nil {
		_ = item.c.Close()
		item.c = nil
	} else if h.closed {
		item.e = mangos.ErrClosed
		_ = item.c.Close()
	}

	h.doneq = append(h.doneq, item)
	h.cv.Broadcast()
}

type dialer struct {
	addr        string
	proto       transport.ProtocolInfo
	hs          *handshaker
	d           *net.Dialer
	config      *tls.Config
	maxRecvSize int
	lock        sync.Mutex
}

func (d *dialer) Dial() (transport.Pipe, error) {
	d.lock.Lock()
	config := d.config
	maxRecvSize := d.maxRecvSize
	d.lock.Unlock()

	tlsDialer := tls.Dialer{NetDialer: d.d, Config: config}

	conn, err := tlsDialer.DialContext(context.Background(), "tcp", d.addr)
	if err != nil {
		return nil, err
	}

	p := newConnPipe(conn, d.proto)
	p.SetOption(mangos.OptionMaxRecvSize, maxRecvSize)

	if tlsConn, ok := conn.(*tls.Conn); ok {
		p.SetOption(mangos.OptionTLSConnState, tlsConn.ConnectionState())
	}

	d.hs.Start(p)

	return d.hs.Wait()
}

//nolint:dupl,gocyclo // Dialers and listeners intentionally support the same transport options.
func (d *dialer) SetOption(n string, v interface{}) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	switch n {
	case mangos.OptionMaxRecvSize:
		maxRecvSize, err := intOption(v)
		if err != nil {
			return err
		}

		d.maxRecvSize = maxRecvSize

		return nil
	case mangos.OptionTLSConfig:
		config, err := tlsConfigOption(v)
		if err != nil {
			return err
		}

		d.config = config

		return nil
	case mangos.OptionKeepAliveTime:
		keepAlive, err := durationOption(v)
		if err != nil {
			return err
		}

		d.d.KeepAlive = keepAlive

		return nil
	case mangos.OptionNoDelay:
		return boolOption(v)
	case mangos.OptionKeepAlive:
		keepAlive, err := keepAliveDuration(v)
		if err != nil {
			return err
		}

		d.d.KeepAlive = keepAlive

		return nil
	}

	return mangos.ErrBadOption
}

func intOption(v interface{}) (int, error) {
	if i, ok := v.(int); ok {
		return i, nil
	}

	return 0, mangos.ErrBadValue
}

func tlsConfigOption(v interface{}) (*tls.Config, error) {
	if config, ok := v.(*tls.Config); ok {
		return config, nil
	}

	return nil, mangos.ErrBadValue
}

func durationOption(v interface{}) (time.Duration, error) {
	if d, ok := v.(time.Duration); ok {
		return d, nil
	}

	return 0, mangos.ErrBadValue
}

func boolOption(v interface{}) error {
	if _, ok := v.(bool); ok {
		return nil
	}

	return mangos.ErrBadValue
}

func keepAliveDuration(v interface{}) (time.Duration, error) {
	b, ok := v.(bool)
	if !ok {
		return 0, mangos.ErrBadValue
	}

	if b {
		return 0, nil
	}

	return -1, nil
}

func (d *dialer) GetOption(n string) (interface{}, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	switch n {
	case mangos.OptionMaxRecvSize:
		return d.maxRecvSize, nil
	case mangos.OptionNoDelay:
		return true, nil
	case mangos.OptionTLSConfig:
		return d.config, nil
	case mangos.OptionKeepAlive:
		return d.d.KeepAlive >= 0, nil
	case mangos.OptionKeepAliveTime:
		return d.d.KeepAlive, nil
	}

	return nil, mangos.ErrBadOption
}

type listener struct {
	addr        string
	bound       net.Addr
	lc          net.ListenConfig
	l           net.Listener
	maxRecvSize int
	proto       transport.ProtocolInfo
	config      *tls.Config
	hs          *handshaker
	closeQ      chan struct{}
	once        sync.Once
	lock        sync.Mutex
}

func (l *listener) Listen() error {
	select {
	case <-l.closeQ:
		return mangos.ErrClosed
	default:
	}

	l.lock.Lock()

	config := l.config
	if config == nil {
		l.lock.Unlock()

		return mangos.ErrTLSNoConfig
	}

	if len(config.Certificates) == 0 {
		l.lock.Unlock()

		return mangos.ErrTLSNoCert
	}

	inner, err := l.lc.Listen(context.Background(), "tcp", l.addr)
	if err != nil {
		l.lock.Unlock()

		return err
	}

	l.l = tls.NewListener(inner, config)
	l.bound = l.l.Addr()
	l.lock.Unlock()

	go l.accept()

	return nil
}

func (l *listener) accept() {
	for {
		conn, err := l.l.Accept()
		if err != nil {
			select {
			case <-l.closeQ:
				return
			default:
				time.Sleep(time.Millisecond)

				continue
			}
		}

		tc, ok := conn.(*tls.Conn)
		if !ok {
			_ = conn.Close()

			continue
		}

		p := newConnPipe(conn, l.proto)
		l.lock.Lock()
		p.SetOption(mangos.OptionMaxRecvSize, l.maxRecvSize)
		p.SetOption(mangos.OptionTLSConnState, tc.ConnectionState())
		l.lock.Unlock()

		l.hs.Start(p)
	}
}

func (l *listener) Address() string {
	if b := l.bound; b != nil {
		return "tls+tcp://" + b.String()
	}

	return "tls+tcp://" + l.addr
}

func (l *listener) Accept() (transport.Pipe, error) {
	if l.l == nil {
		return nil, mangos.ErrClosed
	}

	return l.hs.Wait()
}

func (l *listener) Close() error {
	l.once.Do(func() {
		close(l.closeQ)

		if l.l != nil {
			_ = l.l.Close()
		}

		l.hs.Close()
	})

	return nil
}

//nolint:dupl,gocyclo // Dialers and listeners intentionally support the same transport options.
func (l *listener) SetOption(n string, v interface{}) error {
	l.lock.Lock()
	defer l.lock.Unlock()

	switch n {
	case mangos.OptionMaxRecvSize:
		maxRecvSize, err := intOption(v)
		if err != nil {
			return err
		}

		l.maxRecvSize = maxRecvSize

		return nil
	case mangos.OptionTLSConfig:
		config, err := tlsConfigOption(v)
		if err != nil {
			return err
		}

		l.config = config

		return nil
	case mangos.OptionKeepAliveTime:
		keepAlive, err := durationOption(v)
		if err != nil {
			return err
		}

		l.lc.KeepAlive = keepAlive

		return nil
	case mangos.OptionNoDelay:
		return boolOption(v)
	case mangos.OptionKeepAlive:
		keepAlive, err := keepAliveDuration(v)
		if err != nil {
			return err
		}

		l.lc.KeepAlive = keepAlive

		return nil
	}

	return mangos.ErrBadOption
}

func (l *listener) GetOption(n string) (interface{}, error) {
	l.lock.Lock()
	defer l.lock.Unlock()

	switch n {
	case mangos.OptionMaxRecvSize:
		return l.maxRecvSize, nil
	case mangos.OptionTLSConfig:
		return l.config, nil
	case mangos.OptionKeepAliveTime:
		return l.lc.KeepAlive, nil
	case mangos.OptionNoDelay:
		return true, nil
	case mangos.OptionKeepAlive:
		return l.lc.KeepAlive >= 0, nil
	}

	return nil, mangos.ErrBadOption
}

type tlsTran int

func (t tlsTran) Scheme() string {
	return "tls+tcp"
}

func (t tlsTran) NewDialer(addr string, sock mangos.Socket) (transport.Dialer, error) {
	addr, err := transport.StripScheme(t, addr)
	if err != nil {
		return nil, err
	}

	if _, err = transport.ResolveTCPAddr(addr); err != nil {
		return nil, err
	}

	return &dialer{
		proto: sock.Info(),
		addr:  addr,
		hs:    newHandshaker(),
		d:     &net.Dialer{},
	}, nil
}

func (t tlsTran) NewListener(addr string, sock mangos.Socket) (transport.Listener, error) {
	addr, err := transport.StripScheme(t, addr)
	if err != nil {
		return nil, err
	}

	return &listener{
		proto:  sock.Info(),
		addr:   addr,
		hs:     newHandshaker(),
		closeQ: make(chan struct{}),
	}, nil
}
