package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	dbDelimiter            = "_::_"
	jobStatWindowPercent   = float32(5)
	jobStatWindowScaleThreshold = 100
	recMBRound             = 100
	recSecRound            = 1
)

var (
	bucketJobRAM  = []byte("jobRAM")
	bucketJobDisk = []byte("jobDisk")
	bucketJobSecs = []byte("jobSecs")
	bucketJobsLive = []byte("jobslive")
	bucketRTK      = []byte("repgroupToKey")
)

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
// recovered/run when the manager starts on this DB copy. Stat buckets and the
// complete bucket/counters are left intact so recommendations still work.
func clearLive(path string) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 30 * time.Second})
	if err != nil {
		fmt.Println("open err:", err)
		os.Exit(1)
	}
	defer db.Close()
	var before, after int
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobsLive)
		if b == nil {
			return fmt.Errorf("no jobslive bucket")
		}
		before = b.Stats().KeyN
		c := b.Cursor()
		var keys [][]byte
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			kk := make([]byte, len(k))
			copy(kk, k)
			keys = append(keys, kk)
		}
		for _, k := range keys {
			if e := b.Delete(k); e != nil {
				return e
			}
		}
		after = b.Stats().KeyN
		return nil
	})
	if err != nil {
		fmt.Println("clearlive err:", err)
		os.Exit(1)
	}
	fmt.Printf("jobslive keys: before=%d after=%d\n", before, after)
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
		clearLive(os.Args[2])
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
