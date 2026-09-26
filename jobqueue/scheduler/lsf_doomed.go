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

package scheduler

// doomedSet holds the LSF element ids killExcessCmds has decided to bkill,
// grouped by the job name prefix of the scan that chose them, so a scan only
// ever walks its own prefix's entries. prefixOf indexes each id to its prefix,
// so a claim can check an id in O(1). It is not safe for concurrent use; lsf
// guards it with reservedMu.
type doomedSet struct {
	byPrefix map[string]map[string]struct{}
	prefixOf map[string]string
}

// add records id as doomed by a scan of prefix.
func (d *doomedSet) add(prefix, id string) {
	if d.byPrefix == nil {
		d.byPrefix = make(map[string]map[string]struct{})
		d.prefixOf = make(map[string]string)
	}

	if old, ok := d.prefixOf[id]; ok && old != prefix {
		d.removeFrom(old, id)
	}

	ids := d.byPrefix[prefix]
	if ids == nil {
		ids = make(map[string]struct{})
		d.byPrefix[prefix] = ids
	}

	ids[id] = struct{}{}
	d.prefixOf[id] = prefix
}

// contains reports whether id is doomed.
func (d *doomedSet) contains(id string) bool {
	_, ok := d.prefixOf[id]

	return ok
}

// ofPrefix returns a copy of the ids doomed by scans of prefix.
func (d *doomedSet) ofPrefix(prefix string) map[string]bool {
	ids := make(map[string]bool, len(d.byPrefix[prefix]))
	for id := range d.byPrefix[prefix] {
		ids[id] = true
	}

	return ids
}

// forgetUnseen drops the ids of prefix that a complete scan of it did not see.
func (d *doomedSet) forgetUnseen(prefix string, seen map[string]bool) {
	for id := range d.byPrefix[prefix] {
		if !seen[id] {
			d.removeFrom(prefix, id)
		}
	}
}

// forgetAbsent drops every id not in present, a full snapshot of the elements
// LSF knows about.
func (d *doomedSet) forgetAbsent(present map[string]bool) {
	for id, prefix := range d.prefixOf {
		if !present[id] {
			d.removeFrom(prefix, id)
		}
	}
}

// len returns how many ids are doomed.
func (d *doomedSet) len() int {
	return len(d.prefixOf)
}

func (d *doomedSet) removeFrom(prefix, id string) {
	delete(d.prefixOf, id)

	ids := d.byPrefix[prefix]
	delete(ids, id)

	if len(ids) == 0 {
		delete(d.byPrefix, prefix)
	}
}
