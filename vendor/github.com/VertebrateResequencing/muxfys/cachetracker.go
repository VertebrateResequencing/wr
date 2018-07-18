// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of muxfys.
//
//  muxfys is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  muxfys is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with muxfys. If not, see <http://www.gnu.org/licenses/>.

package muxfys

// This file implements a system for tracking which byte intervals have already
// been downloaded, as needed by NewCachedFile. (Method names all include the
// word 'cache' so that you can embed a CacheTracker and not get confused by
// the ambiguity of the verbs.)

import (
	"sync"
)

// CacheTracker struct is used to track what parts of which files have been
// cached.
type CacheTracker struct {
	sync.Mutex
	cached map[string]Intervals
}

// NewCacheTracker creates a new *CacheTracker.
func NewCacheTracker() *CacheTracker {
	return &CacheTracker{cached: make(map[string]Intervals)}
}

// Cached updates the tracker with what you have now cached. Once you have
// stored bytes 0..9 in /abs/path/to/sparse.file, you would call:
// Cached("/abs/path/to/sparse.file", NewInterval(0, 10)).
func (c *CacheTracker) Cached(path string, iv Interval) {
	c.Lock()
	defer c.Unlock()
	c.cached[path] = c.cached[path].Merge(iv)
}

// Uncached tells you what parts of a file in the given interval you haven't
// already cached (based on your prior Cached() calls). You would want to then
// cache the data in each of the returned intervals and call Cached() on each
// one afterwards.
func (c *CacheTracker) Uncached(path string, iv Interval) Intervals {
	c.Lock()
	defer c.Unlock()
	return c.cached[path].Difference(iv)
}

// CacheTruncate should be used to update the tracker if you truncate a cache
// file. The internal knowledge of what you have cached for that file will then
// be updated to exclude anything beyond the truncation point.
func (c *CacheTracker) CacheTruncate(path string, offset int64) {
	c.Lock()
	defer c.Unlock()
	c.cached[path] = c.cached[path].Truncate(offset)
}

// CacheOverride should be used if you do something like delete a cache file and
// then recreate it and cache some data inside it. This is the slightly more
// efficient alternative to calling Delete(path) followed by Cached(path, iv).
func (c *CacheTracker) CacheOverride(path string, iv Interval) {
	c.Lock()
	defer c.Unlock()
	c.cached[path] = Intervals{iv}
}

// CacheRename should be used if you rename a cache file on disk.
func (c *CacheTracker) CacheRename(oldPath, newPath string) {
	c.Lock()
	defer c.Unlock()
	c.cached[newPath] = c.cached[oldPath]
	delete(c.cached, oldPath)
}

// CacheDelete should be used if you delete a cache file.
func (c *CacheTracker) CacheDelete(path string) {
	c.Lock()
	defer c.Unlock()
	delete(c.cached, path)
}

// CacheWipe should be used if you delete all your cache files.
func (c *CacheTracker) CacheWipe() {
	c.Lock()
	defer c.Unlock()
	c.cached = make(map[string]Intervals)
}
