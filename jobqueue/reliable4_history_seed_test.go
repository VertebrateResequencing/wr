//go:build reliability_repro

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

// DB GENERATOR for the reliable4 FINDING 1 scale gate
// (.docs/reliable4/prod-run-20260817.md): it builds a manager database whose
// ARCHIVED history is large, so that developers/wrdev.sh control-rpc-history can
// point a REAL wr manager at it and time the REAL `wr limit` / `wr suspend -i` /
// `wr resume -i -z` commands against it.
//
// The history is bulk-inserted straight into the three buckets archiveJob writes
// and retrieveCompleteJobsByRepGroup reads (complete records, the RepGroup+key
// lookup index, and the rep-groups list), because running hundreds of thousands of
// jobs through the queue to get the same shape would take hours. Nothing else is
// pre-populated: the manager under test adds its own live jobs.
//
// Production's shape, for reference: ~2.15M complete records averaging ~1.5KB
// against ~118k live jobs, and `wr resume -i portal -z` over that history ran
// CPU-bound for 12+ minutes and took the manager's heap from 0.35GB to 12.1GB.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	historySeedDefaultArchived = 400000
	historySeedDefaultGroups   = 20

	// historySeedPadding pads each archived job's Cwd so its encoded record is
	// production-sized (~1.5KB) rather than the ~200 bytes a minimal test job
	// encodes to. Record size is what turns a decode-everything scan into a heap
	// excursion, so a gate seeded with tiny records would understate the bug.
	historySeedPadding = 1200
)

// TestReliable4SeedArchivedHistory writes a database with WR_HS_ARCHIVED archived
// jobs spread evenly over WR_HS_GROUPS RepGroups named <WR_HS_RG_PREFIX><n>, at
// WR_HS_DB. It is a generator, not an assertion: developers/wrdev.sh
// control-rpc-history runs it and then measures a real manager serving that DB.
func TestReliable4SeedArchivedHistory(t *testing.T) {
	dbFile := os.Getenv("WR_HS_DB")
	if dbFile == "" {
		t.Skip("set WR_HS_DB to the database file to create (see wrdev.sh control-rpc-history)")
	}

	archived := historySeedEnvInt("WR_HS_ARCHIVED", historySeedDefaultArchived)
	groups := historySeedEnvInt("WR_HS_GROUPS", historySeedDefaultGroups)
	prefix := os.Getenv("WR_HS_RG_PREFIX")

	if prefix == "" {
		prefix = "hsrg"
	}

	if groups < 1 || archived < groups {
		t.Fatalf("need WR_HS_ARCHIVED (%d) >= WR_HS_GROUPS (%d) >= 1", archived, groups)
	}

	perGroup := archived / groups
	started := time.Now()

	seeded, size := seedHistoryDB(t, dbFile, prefix, groups, perGroup)

	fmt.Printf("HISTORY-SEED archived=%d groups=%d perGroup=%d bytes=%d recordBytes=%d seconds=%.1f db=%s\n",
		seeded, groups, perGroup, size, size/int64(seeded), time.Since(started).Seconds(), dbFile)
}

func historySeedEnvInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}

	return value
}

// seedHistoryDB creates dbFile and bulk-inserts perGroup archived jobs into each
// of groups RepGroups, returning how many records it wrote and the size of the
// resulting file.
func seedHistoryDB(t *testing.T, dbFile, prefix string, groups, perGroup int) (int, int64) {
	t.Helper()

	ctx := context.Background()

	testDB, _, err := initDB(ctx, dbFile, dbFile+"_bk", internal.Development, false, false)
	if err != nil {
		t.Fatalf("could not create %s: %s", dbFile, err)
	}

	var encoded []byte
	if err = codec.NewEncoderBytes(&encoded, testDB.ch).Encode(historySeedJob(prefix)); err != nil {
		t.Fatalf("could not encode an archived job: %s", err)
	}

	seeded := 0

	for g := range groups {
		repGroup := prefix + strconv.Itoa(g)

		// one transaction per RepGroup: one giant transaction for the whole history
		// would need the whole thing in memory as dirty pages.
		err = testDB.bolt.Update(func(tx *bolt.Tx) error {
			return seedHistoryRepGroup(tx, testDB, repGroup, encoded, perGroup)
		})
		if err != nil {
			t.Fatalf("could not seed %s: %s", repGroup, err)
		}

		seeded += perGroup
	}

	if err = testDB.close(ctx); err != nil {
		t.Fatalf("could not close %s: %s", dbFile, err)
	}

	info, err := os.Stat(dbFile)
	if err != nil {
		t.Fatalf("could not stat %s: %s", dbFile, err)
	}

	return seeded, info.Size()
}

// historySeedJob returns a completed job whose encoded size is production-like.
func historySeedJob(prefix string) *Job {
	pad := make([]byte, historySeedPadding)
	for i := range pad {
		pad[i] = 'd'
	}

	now := time.Now()

	return &Job{
		Cmd: "bash -c 'set -euo pipefail; compress --input /lustre/scratch/" + string(pad[:200]) +
			" --output /lustre/scratch/out --threads 4'",
		Cwd:          "/lustre/scratch/" + string(pad),
		ReqGroup:     prefix + "-reqgroup",
		RepGroup:     prefix,
		Requirements: &jqs.Requirements{RAM: 2000, Time: time.Hour, Cores: 2, Disk: 10, Other: make(map[string]string)},
		DepGroups:    []string{prefix + "-depgroup"},
		LimitGroups:  []string{prefix + "-limitgroup"},
		State:        JobStateComplete,
		Exited:       true,
		Exitcode:     0,
		StartTime:    now.Add(-time.Hour),
		EndTime:      now,
		PeakRAM:      1500,
		PeakDisk:     8,
		CPUtime:      time.Hour,
		Attempts:     1,
	}
}

// seedHistoryRepGroup writes n archived records for repGroup inside tx, in every
// bucket archiveJobTx writes to for a RepGroup: the records themselves, the
// RepGroup+key lookup index, the rep-groups list and the RepGroup's end time.
func seedHistoryRepGroup(tx *bolt.Tx, testDB *db, repGroup string, encoded []byte, n int) error {
	completeBucket := tx.Bucket(bucketJobsComplete)
	lookupBucket := tx.Bucket(bucketRTK)

	if err := tx.Bucket(bucketRGs).Put([]byte(repGroup), nil); err != nil {
		return err
	}

	if err := updateRGEndTime(tx.Bucket(bucketRGEndTime),
		&Job{RepGroup: repGroup, EndTime: time.Now()}); err != nil {
		return err
	}

	for i := range n {
		key := []byte(repGroup + "-" + strconv.Itoa(i))

		if err := completeBucket.Put(key, encoded); err != nil {
			return err
		}

		if err := lookupBucket.Put(testDB.generateLookupKey(repGroup, key), nil); err != nil {
			return err
		}
	}

	return nil
}
