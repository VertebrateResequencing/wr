package main

import (
	"fmt"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "topgroups" {
		topgroups()
		return
	}
	path := os.Args[1]
	t0 := time.Now()
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: 30 * time.Second})
	if err != nil {
		fmt.Println("OPEN ERROR:", err)
		os.Exit(1)
	}
	defer db.Close()
	fmt.Printf("opened read-only in %v\n", time.Since(t0))

	upgradeBuckets := map[string]bool{"depgroups": false, "jobLookupEntries": false}
	countBuckets := map[string]bool{"jobslive": true, "jobscomplete": true, "repgroupToKey": true, "repgroups": true, "depgroupToKey": true}

	_ = db.View(func(tx *bolt.Tx) error {
		fmt.Println("--- top-level buckets present ---")
		_ = tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			n := string(name)
			if _, ok := upgradeBuckets[n]; ok {
				upgradeBuckets[n] = true
			}
			fmt.Printf("  %-22s\n", n)
			return nil
		})
		fmt.Println("--- upgrade-trigger buckets (present => no rebuild) ---")
		for n, present := range upgradeBuckets {
			fmt.Printf("  %-22s present=%v\n", n, present)
		}
		fmt.Println("--- key counts (Stats.KeyN) ---")
		for n := range countBuckets {
			b := tx.Bucket([]byte(n))
			if b == nil {
				fmt.Printf("  %-22s ABSENT\n", n)
				continue
			}
			ts := time.Now()
			st := b.Stats()
			fmt.Printf("  %-22s keys=%d  (stats took %v)\n", n, st.KeyN, time.Since(ts))
		}
		return nil
	})
}
