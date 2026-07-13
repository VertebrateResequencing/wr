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

import (
	"bytes"
	"fmt"
	"slices"
	"sort"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

func persistedRepGroupsForJobKey(reverse *bolt.Bucket, jobKey []byte) []string {
	prefix := reverseLookupEntryPrefix(jobKey)
	cursor := reverse.Cursor()
	repGroups := make([]string, 0)

	for entry, _ := cursor.Seek(prefix); bytes.HasPrefix(entry, prefix); entry, _ = cursor.Next() {
		lookupBucket, lookupKey, ok := parseReverseLookupEntry(entry, prefix)
		if !ok || !bytes.Equal(lookupBucket, bucketRTK) || !bytes.Equal(lookupEntryJobKey(lookupKey), jobKey) {
			continue
		}

		idx := bytes.LastIndex(lookupKey, []byte(dbDelimiter))
		if idx > 0 {
			repGroups = append(repGroups, string(lookupKey[:idx]))
		}
	}

	sort.Strings(repGroups)

	return slices.Compact(repGroups)
}

type persistedJobStatusGroups struct {
	complete  bool
	repGroups []string
}

// retrievePersistedJobStatusGroups returns every historical RepGroup lookup
// for each key and whether that key still has an archived record. The reverse
// lookup index makes this proportional to the supplied jobs' own lookups rather
// than to the full historical RepGroup index.
func (db *db) retrievePersistedJobStatusGroups(keys []string) (map[string]persistedJobStatusGroups, error) {
	statusGroups := make(map[string]persistedJobStatusGroups, len(keys))

	err := db.bolt.View(func(tx *bolt.Tx) error {
		reverse := tx.Bucket(bucketJobLookupEntries)
		if reverse == nil {
			return fmt.Errorf("%w: %s", berrors.ErrBucketNotFound, bucketJobLookupEntries)
		}

		complete := tx.Bucket(bucketJobsComplete)

		for _, key := range keys {
			if _, done := statusGroups[key]; done {
				continue
			}

			jobKey := []byte(key)
			statusGroups[key] = persistedJobStatusGroups{
				complete:  complete.Get(jobKey) != nil,
				repGroups: persistedRepGroupsForJobKey(reverse, jobKey),
			}
		}

		return nil
	})

	return statusGroups, err
}

// markPersistedJobStatusGroups records every historical RepGroup lookup for
// jobs. When moveFromComplete is true, an archived record also marks the first
// queue transition as a move out of every completed contribution.
func (s *Server) markPersistedJobStatusGroups(jobs []*Job, moveFromComplete bool) error {
	keys := make([]string, 0, len(jobs))
	for _, job := range jobs {
		keys = append(keys, job.Key())
	}

	statusGroups, err := s.db.retrievePersistedJobStatusGroups(keys)
	if err != nil {
		return err
	}

	for _, job := range jobs {
		persisted, ok := statusGroups[job.Key()]
		if !ok {
			continue
		}

		job.Lock()
		job.statusFromComplete = moveFromComplete && persisted.complete
		job.statusCompleteRepGroups = persisted.repGroups
		job.Unlock()
	}

	return nil
}
