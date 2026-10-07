// Package jobgen makes realistic wr jobs for the prototypes: field sizes
// follow the production fixture (dbstats on prod.db) and the soak's 10KB
// portal commands.
package jobgen

import (
	"crypto/md5" //nolint:gosec // wr's keys are md5s
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

// Job returns job i with a command of about cmdSize bytes.
func Job(i, cmdSize int) *jobqueue.Job {
	prefix := fmt.Sprintf("portal_dedupe --input /lustre/scratch125/humgen/projects/x/%08d.cram --out ", i)
	cmd := prefix + strings.Repeat("a", max(cmdSize-len(prefix), 0))

	return &jobqueue.Job{
		Cmd:          cmd,
		Cwd:          "/lustre/scratch125/humgen/teams/hgi/wr/portal/work/dir",
		RepGroup:     fmt.Sprintf("portal_dedupe 20260929T2223%02d", i%60),
		ReqGroup:     "portal_dedupe",
		Requirements: &scheduler.Requirements{RAM: 1500, Time: time.Hour, Cores: 1, CoresSet: true},
		Retries:      3,
		Priority:     5,
		LimitGroups:  []string{"portal"},
		DepGroups:    []string{fmt.Sprintf("dg%d", i%100)},
		EnvKey:       "8f2b1c6c4e1f4a8b9c0d1e2f3a4b5c6d",
		State:        jobqueue.JobStateReady,
		Exitcode:     -1,
	}
}

// Key returns job i's key as wr makes it (md5 hex of cwd and cmd).
func Key(j *jobqueue.Job) string {
	sum := md5.Sum([]byte(j.Cwd + j.Cmd)) //nolint:gosec

	return hex.EncodeToString(sum[:])
}
