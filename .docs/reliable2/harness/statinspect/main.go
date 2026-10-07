package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	dbDelimiter                 = "_::_"
	jobStatWindowPercent        = float32(5)
	jobStatWindowScaleThreshold = 100
	recMBRound                  = 100
	recSecRound                 = 1
)

var (
	bucketJobRAM      = []byte("jobRAM")
	bucketJobDisk     = []byte("jobDisk")
	bucketJobSecs     = []byte("jobSecs")
	bucketJobsLive    = []byte("jobslive")
	bucketJobRunState = []byte("jobRunState")
	bucketRTK         = []byte("repgroupToKey")

	errNoJobsLive = errors.New("no jobslive bucket")
)

// clearCounts holds the key counts clearBuckets found before deleting.
type clearCounts struct {
	liveBefore  int
	rsBefore    int
	hasRunState bool
}

// clearBuckets deletes every jobslive key and, if that bucket exists, every
// jobRunState key, returning how many of each there were.
func clearBuckets(tx *bolt.Tx) (clearCounts, error) {
	var counts clearCounts

	b := tx.Bucket(bucketJobsLive)
	if b == nil {
		return counts, errNoJobsLive
	}

	var err error
	if counts.liveBefore, err = deleteAllKeys(b); err != nil {
		return counts, err
	}

	rs := tx.Bucket(bucketJobRunState)
	if rs == nil {
		return counts, nil
	}

	counts.hasRunState = true
	counts.rsBefore, err = deleteAllKeys(rs)

	return counts, err
}

// reportCounts prints the before counts in c next to after counts read in a
// fresh transaction, so they reflect the committed deletes.
func reportCounts(db *bolt.DB, out io.Writer, c clearCounts) error {
	var liveAfter, rsAfter int

	err := db.View(func(tx *bolt.Tx) error {
		liveAfter = countKeys(tx.Bucket(bucketJobsLive))
		rsAfter = countKeys(tx.Bucket(bucketJobRunState))

		return nil
	})
	if err != nil {
		return fmt.Errorf("clearlive count: %w", err)
	}

	fmt.Fprintf(out, "jobslive keys: before=%d after=%d\n", c.liveBefore, liveAfter)

	if c.hasRunState {
		fmt.Fprintf(out, "jobRunState keys: before=%d after=%d\n", c.rsBefore, rsAfter)
	}

	return nil
}

// countKeys counts b's keys with a cursor; a nil bucket has none.
func countKeys(b *bolt.Bucket) int {
	if b == nil {
		return 0
	}

	n := 0

	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		n++
	}

	return n
}

// deleteAllKeys deletes every key in b, returning how many there were.
func deleteAllKeys(b *bolt.Bucket) (int, error) {
	c := b.Cursor()

	keys := make([][]byte, 0, countKeys(b))
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		keys = append(keys, bytes.Clone(k))
	}

	for _, k := range keys {
		if err := b.Delete(k); err != nil {
			return 0, err
		}
	}

	return len(keys), nil
}

// replicate scanReqGroupStat
func scanReqGroupStat(c *bolt.Cursor, prefix []byte) (maxVal, recommendation, count int) {
	window := jobStatWindowPercent
	var prev []int
	for k, v := c.Seek(prefix); bytes.HasPrefix(k, prefix); k, v = c.Next() {
		mv, err := strconv.Atoi(string(v))
		if err != nil {
			continue
		}
		maxVal = mv
		count++
		if count > jobStatWindowScaleThreshold {
			window = (float32(count) / jobStatWindowScaleThreshold) * jobStatWindowPercent
		}
		prev = append(prev, mv)
		if float32(len(prev)) > window {
			recommendation, prev = prev[0], prev[1:]
		}
	}
	return
}

func roundRecommendation(recommendation, maxVal, roundAmount int) int {
	if recommendation == 0 {
		if maxVal == 0 {
			return 0
		}
		recommendation = maxVal
	}
	if maxVal-recommendation < roundAmount {
		recommendation = maxVal
	}
	if recommendation < roundAmount {
		recommendation = roundAmount
	}
	if recommendation%roundAmount > 0 {
		recommendation = int(math.Ceil(float64(recommendation)/float64(roundAmount))) * roundAmount
	}
	return recommendation
}

// clearLive empties the jobslive bucket so no incomplete/production jobs are
// recovered/run when the manager starts on this DB copy, and the jobRunState
// bucket (if present) so no stale run-state records survive either. Stat
// buckets and the complete bucket/counters are left intact so recommendations
// still work.
func clearLive(path string, out io.Writer) error {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	var counts clearCounts

	err = db.Update(func(tx *bolt.Tx) error {
		var errc error

		counts, errc = clearBuckets(tx)

		return errc
	})
	if err != nil {
		return err // main prefixes "clearlive err:"
	}

	return reportCounts(db, out, counts)
}

func stat(db *bolt.DB, bucket []byte, reqGroup string, round int) (max, rec, count int) {
	db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return nil
		}
		max, rec, count = scanReqGroupStat(b.Cursor(), []byte(reqGroup))
		return nil
	})
	return max, roundRecommendation(rec, max, round), count
}

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "clearlive" {
		if err := clearLive(os.Args[2], os.Stdout); err != nil {
			fmt.Println("clearlive err:", err)
			os.Exit(1)
		}
		return
	}
	path := os.Args[1]
	reqGroups := os.Args[2:]
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: 10 * time.Second})
	if err != nil {
		fmt.Println("open err:", err)
		os.Exit(1)
	}
	defer db.Close()

	// count incomplete (live) jobs
	var live int
	db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobsLive)
		if b != nil {
			live = b.Stats().KeyN
		}
		return nil
	})
	fmt.Printf("INCOMPLETE (jobslive) jobs in DB: %d\n\n", live)

	fmt.Printf("%-28s %10s %12s %12s | %10s %12s %10s\n", "reqGroup", "RAM_n", "RAM_max(MB)", "RAM_rec(MB)", "Secs_n", "Secs_max", "Secs_rec")
	for _, rg := range reqGroups {
		rmax, rrec, rn := stat(db, bucketJobRAM, rg, recMBRound)
		_, drec, dn := stat(db, bucketJobDisk, rg, recMBRound)
		smax, srec, sn := stat(db, bucketJobSecs, rg, recSecRound)
		fmt.Printf("%-28s %10d %12d %12d | %10d %12d %10d  (disk_n=%d disk_rec=%d)\n",
			rg, rn, rmax, rrec, sn, smax, srec, dn, drec)
	}
}
