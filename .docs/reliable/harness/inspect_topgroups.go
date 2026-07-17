package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

func topgroups() {
	path := os.Args[2]
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: 30 * time.Second})
	if err != nil {
		fmt.Println("OPEN ERROR:", err)
		os.Exit(1)
	}
	defer db.Close()
	delim := []byte("_::_")
	counts := map[string]int{}
	_ = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("repgroupToKey"))
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			idx := bytes.LastIndex(k, delim)
			if idx > 0 {
				counts[string(k[:idx])]++
			}
		}
		return nil
	})
	type rg struct {
		name string
		n    int
	}
	list := make([]rg, 0, len(counts))
	for n, c := range counts {
		list = append(list, rg{n, c})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	fmt.Printf("total repgroups=%d\n", len(list))
	for i := 0; i < 20 && i < len(list); i++ {
		fmt.Printf("  %8d  %s\n", list[i].n, list[i].name)
	}
	// also dump top 60 names to a file for reuse
	f, _ := os.Create("/tmp/wr-reliable/top-repgroups.txt")
	for i := 0; i < 60 && i < len(list); i++ {
		fmt.Fprintln(f, list[i].name)
	}
	f.Close()
}
