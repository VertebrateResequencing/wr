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

// Command fusestall is soak tooling, not part of wr: a FUSE loopback mount of
// a directory whose fsyncs block while a control file exists, so a manager
// whose database is opened through the mount has its commits stalled on
// demand, as a full or hung file system would, and resumes when the file is
// removed. Nothing is stored in the mount: every byte goes to the underlying
// directory. It needs FUSE (/dev/fuse and fusermount).
//
// usage: fusestall -dir <underlying> -mnt <mountpoint> -ctl <file> [-max 10m] [-coherent=true]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

const (
	exitUsage      = 2
	pollEvery      = 200 * time.Millisecond
	defaultMaxHold = 10 * time.Minute
)

var errUsage = errors.New("-dir, -mnt and -ctl are required")

var _ = (fs.NodeWrapChilder)((*node)(nil))

var _ = (fs.NodeOpener)((*node)(nil))

var _ = (fs.NodeCreater)((*node)(nil))

// stall is the mount's shared state: which file holds fsyncs, for how long at
// most, how many are held now, and the open flags every open returns.
type stall struct {
	ctlFile string
	maxHold time.Duration
	held    atomic.Int64
	// keepCache is returned from every open when -coherent is set, so that an
	// open() of a file does not make the kernel drop that file's cached pages.
	keepCache uint32
}

func parseFlags() (string, string, *stall, error) {
	st := &stall{}
	dir := flag.String("dir", "", "underlying directory")
	mnt := flag.String("mnt", "", "mountpoint")

	flag.StringVar(&st.ctlFile, "ctl", "", "fsyncs block while this file exists")
	flag.DurationVar(&st.maxHold, "max", defaultMaxHold, "longest any one fsync is held")

	coherent := flag.Bool("coherent", true, "keep the kernel page cache coherent for mmap readers: no cache "+
		"invalidation on open() or on a changed mtime (see the comment at the mount)")

	flag.Parse()

	if *coherent {
		st.keepCache = fuse.FOPEN_KEEP_CACHE
	}

	if *dir == "" || *mnt == "" || st.ctlFile == "" {
		return "", "", nil, errUsage
	}

	return *dir, *mnt, st, nil
}

func main() {
	dir, mnt, st, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fusestall:", err)
		flag.Usage()
		os.Exit(exitUsage)
	}

	if err = serve(dir, mnt, st); err != nil {
		fmt.Fprintln(os.Stderr, "fusestall:", err)
		os.Exit(1)
	}
}

// serve mounts dir at mnt until SIGINT or SIGTERM unmounts it.
func serve(dir, mnt string, st *stall) error {
	var sst syscall.Stat_t
	if err := syscall.Stat(dir, &sst); err != nil {
		return err
	}

	root := &fs.LoopbackRoot{Path: dir, Dev: sst.Dev}
	rootNode := &node{LoopbackNode: &fs.LoopbackNode{RootData: root}, st: st}
	root.RootNode = rootNode
	sec := time.Second

	server, err := fs.Mount(mnt, rootNode, &fs.Options{
		EntryTimeout: &sec, AttrTimeout: &sec,
		// A write through FUSE copies into the page cache and unlocks the page
		// before this daemon has written it to the underlying file. If the kernel
		// invalidates the file's pages in that window (it does on every open()
		// without FOPEN_KEEP_CACHE, and, with auto_inval_data, whenever a stat
		// sees a new mtime), an mmap reader faults the page back in from the
		// underlying file and keeps the OLD bytes. bbolt reads its pages through
		// mmap, so it then walks stale pages and panics "page N already freed".
		// Every byte reaches the file through this mount, so there is nothing
		// to invalidate for: turn both off.
		MountOptions: fuse.MountOptions{FsName: "fusestall", Name: "fusestall", MaxWrite: fuse.MAX_KERNEL_WRITE,
			ExplicitDataCacheControl: st.keepCache != 0},
	})
	if err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	fmt.Fprintf(os.Stderr, "%s mounted %s at %s; fsyncs block while %s exists\n",
		time.Now().Format(time.RFC3339), dir, mnt, st.ctlFile)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go unmountOnSignal(server, sigs)

	server.Wait()

	return nil
}

func unmountOnSignal(server *fuse.Server, sigs <-chan os.Signal) {
	<-sigs
	fmt.Fprintln(os.Stderr, "fusestall: unmounting")

	if err := server.Unmount(); err != nil {
		fmt.Fprintln(os.Stderr, "fusestall: unmount:", err)
	}
}

type file struct {
	*fs.LoopbackFile

	name string
	st   *stall
}

// PassthroughFd is refused so every operation comes through this daemon.
func (f *file) PassthroughFd() (int, bool) { return 0, false }

// Fsync waits while the control file exists (at most -max), then syncs.
func (f *file) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	t0 := time.Now()
	logged := false

	for time.Since(t0) < f.st.maxHold {
		if _, err := os.Stat(f.st.ctlFile); err != nil {
			break
		}

		if !logged {
			logged = true

			fmt.Fprintf(os.Stderr, "%s holding fsync #%d of %s\n", time.Now().Format(time.RFC3339),
				f.st.held.Add(1), f.name)
		}

		time.Sleep(pollEvery)
	}

	if logged {
		f.st.held.Add(-1)
		fmt.Fprintf(os.Stderr, "%s released fsync of %s after %s\n", time.Now().Format(time.RFC3339), f.name,
			time.Since(t0).Round(time.Millisecond))
	}

	return f.LoopbackFile.Fsync(ctx, flags)
}

// node is a loopback node whose files' fsyncs can stall.
type node struct {
	*fs.LoopbackNode

	st *stall
}

// WrapChild makes every node below the root a node too.
func (n *node) WrapChild(_ context.Context, ops fs.InodeEmbedder) fs.InodeEmbedder {
	ln, ok := ops.(*fs.LoopbackNode)
	if !ok {
		return ops
	}

	return &node{LoopbackNode: ln, st: n.st}
}

// Open opens the underlying file and returns a handle whose Fsync can stall.
func (n *node) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	flags &^= syscall.O_APPEND | fuse.FMODE_EXEC
	p := filepath.Join(n.RootData.Path, n.Path(n.Root()))

	fd, err := syscall.Open(p, int(flags), 0)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}

	lf := fs.NewLoopbackFileFromOS(os.NewFile(uintptr(fd), p))

	return &file{LoopbackFile: lf, name: p, st: n.st}, n.st.keepCache, 0
}

// Create creates the underlying file and returns a handle whose Fsync can
// stall.
func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32,
	out *fuse.EntryOut,
) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	inode, fh, fuseFlags, errno := n.LoopbackNode.Create(ctx, name, flags, mode, out)
	if errno != 0 {
		return inode, fh, fuseFlags, errno
	}

	if lf, ok := fh.(*fs.LoopbackFile); ok {
		fh = &file{LoopbackFile: lf, name: filepath.Join(n.RootData.Path, n.Path(n.Root()), name), st: n.st}
	}

	return inode, fh, fuseFlags | n.st.keepCache, 0
}
