// Command dbstats prints per-bucket key counts and sizes of a wr manager
// database, and field-size statistics of the jobs it holds, so the customdb
// designs can be sized against real data. It opens the file read-only.
package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

type sizes struct {
	n          int
	keyBytes   int
	valBytes   int
	vals       []int
	nested     int
}

func (s *sizes) add(k, v []byte) {
	s.n++
	s.keyBytes += len(k)
	s.valBytes += len(v)
	s.vals = append(s.vals, len(v))
}

func pct(v []int, p float64) int {
	if len(v) == 0 {
		return 0
	}

	slices.Sort(v)

	return v[int(float64(len(v)-1)*p)]
}

func main() {
	path := flag.String("db", "", "bolt db path")
	sample := flag.Int("sample", 200000, "max jobs to decode per job bucket")
	flag.Parse()

	bdb, err := bolt.Open(*path, 0o400, &bolt.Options{ReadOnly: true, Timeout: 5 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer bdb.Close()

	ch := new(codec.BincHandle)

	err = bdb.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			s := &sizes{}
			_ = b.ForEach(func(k, v []byte) error {
				if v == nil {
					s.nested++
				}
				s.add(k, v)

				return nil
			})
			fmt.Printf("bucket %-22s keys=%-9d keyB=%-11d valB=%-12d val p50=%d p90=%d p99=%d max=%d\n",
				name, s.n, s.keyBytes, s.valBytes, pct(s.vals, 0.5), pct(s.vals, 0.9), pct(s.vals, 0.99), pct(s.vals, 1))

			if string(name) == "jobslive" || string(name) == "jobscomplete" {
				jobStats(ch, b, *sample)
			}

			return nil
		})
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func jobStats(ch *codec.BincHandle, b *bolt.Bucket, sample int) {
	var cmd, cwd, rg, deps, depgs, lgs, mounts, other []int

	rgs := map[string]int{}
	states := map[jobqueue.JobState]int{}
	n := 0
	start := time.Now()

	c := b.Cursor()
	for k, v := c.First(); k != nil && n < sample; k, v = c.Next() {
		var j jobqueue.Job
		if err := codec.NewDecoderBytes(v, ch).Decode(&j); err != nil {
			continue
		}

		n++
		cmd = append(cmd, len(j.Cmd))
		cwd = append(cwd, len(j.Cwd))
		rg = append(rg, len(j.RepGroup))
		deps = append(deps, len(j.Dependencies))
		depgs = append(depgs, len(j.DepGroups))
		lgs = append(lgs, len(j.LimitGroups))
		mounts = append(mounts, len(j.MountConfigs))
		other = append(other, len(v)-len(j.Cmd))
		rgs[j.RepGroup]++
		states[j.State]++
	}

	el := time.Since(start)
	fmt.Printf("  decoded %d jobs in %s (%.1fus/job)\n", n, el, float64(el.Microseconds())/float64(max(n, 1)))
	fmt.Printf("  cmd p50=%d p90=%d p99=%d max=%d; nonCmd bytes p50=%d p99=%d; cwd p50=%d; repgroup len p50=%d\n",
		pct(cmd, .5), pct(cmd, .9), pct(cmd, .99), pct(cmd, 1), pct(other, .5), pct(other, .99), pct(cwd, .5), pct(rg, .5))
	fmt.Printf("  deps p50=%d max=%d; depgroups p50=%d max=%d; limitgroups p50=%d max=%d; mounts max=%d; distinct repgroups=%d\n",
		pct(deps, .5), pct(deps, 1), pct(depgs, .5), pct(depgs, 1), pct(lgs, .5), pct(lgs, 1), pct(mounts, 1), len(rgs))

	var counts []int
	for _, v := range rgs {
		counts = append(counts, v)
	}

	fmt.Printf("  jobs per repgroup p50=%d p99=%d max=%d; states=%v\n", pct(counts, .5), pct(counts, .99), pct(counts, 1), states)
}
