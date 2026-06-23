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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	cgroupOOMKillKey   = "oom_kill"
	cgroupV2OOMKillKey = cgroupOOMKillKey
	cgroupV1OOMKillKey = cgroupOOMKillKey
	cgroupLineParts    = 3
	cgroupOOMMissing   = "cgroup OOM kill count missing"
)

const (
	procRoot   = "/proc"
	cgroupRoot = "/sys/fs/cgroup"
)

const shellSignalExitCodeOffset = 128

type cgroupOOMCounter struct {
	path    string
	key     string
	initial uint64
}

func cgroupOOMCounterFromLine(line, root string) (cgroupOOMCounter, bool) {
	parts := strings.Split(line, ":")
	if len(parts) != cgroupLineParts {
		return cgroupOOMCounter{}, false
	}

	cgroupPath := strings.TrimPrefix(parts[2], "/")
	if parts[0] == "0" && parts[1] == "" {
		return cgroupOOMCounter{
			path: filepath.Join(root, cgroupPath, "memory.events"),
			key:  cgroupV2OOMKillKey,
		}, true
	}

	controllers := strings.Split(parts[1], ",")
	for _, controller := range controllers {
		if controller == "memory" {
			return cgroupOOMCounter{
				path: filepath.Join(root, "memory", cgroupPath, "memory.oom_control"),
				key:  cgroupV1OOMKillKey,
			}, true
		}
	}

	return cgroupOOMCounter{}, false
}

type cgroupOOMKillCountMissingError struct {
	key  string
	path string
}

func (err cgroupOOMKillCountMissingError) Error() string {
	return fmt.Sprintf("%s: %s in %s", cgroupOOMMissing, err.key, err.path)
}

type cgroupOOMMonitor struct {
	counters []cgroupOOMCounter
}

func newCgroupOOMMonitor(pid int, proc, cgroup string) (*cgroupOOMMonitor, error) {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return nil, err
	}

	var counters []cgroupOOMCounter

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		counter, ok := cgroupOOMCounterFromLine(line, cgroup)
		if !ok {
			continue
		}

		count, errc := readCgroupOOMKillCount(counter.path, counter.key)
		if errc != nil {
			continue
		}

		counter.initial = count
		counters = append(counters, counter)
	}

	if len(counters) == 0 {
		return nil, os.ErrNotExist
	}

	return &cgroupOOMMonitor{counters: counters}, nil
}

func (m *cgroupOOMMonitor) oomKillIncreased() bool {
	if m == nil {
		return false
	}

	for _, counter := range m.counters {
		current, err := readCgroupOOMKillCount(counter.path, counter.key)
		if err != nil {
			continue
		}

		if current > counter.initial {
			return true
		}
	}

	return false
}

func readCgroupOOMKillCount(path, key string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != key {
			continue
		}

		count, errp := strconv.ParseUint(fields[1], 10, 64)
		if errp != nil {
			return 0, errp
		}

		return count, nil
	}

	return 0, cgroupOOMKillCountMissingError{key: key, path: path}
}

func appendHighMemoryNote(err error, peakRAM, requiredRAM int) error {
	if err == nil || !commandExceededMemoryEstimate(peakRAM, requiredRAM) {
		return err
	}

	return fmt.Errorf("%w; note: %s", err, FailReasonRAM)
}

func commandExceededMemoryEstimate(peakRAM, requiredRAM int) bool {
	return peakRAM > requiredRAM
}

func attributedMemoryDeath(
	wrKilledForMemory bool,
	cgroupOOM bool,
	status syscall.WaitStatus,
	peakRAM int,
	requiredRAM int,
	scheduler string,
	allowSchedulerFallback bool,
) bool {
	if cgroupOOM {
		return oomCompatibleExitStatus(status)
	}

	if wrKilledForMemory {
		return sigkillMemoryFallback(status, peakRAM, requiredRAM)
	}

	return allowSchedulerFallback &&
		schedulerNeedsSigkillMemoryFallback(scheduler) &&
		sigkillMemoryFallback(status, peakRAM, requiredRAM)
}

func oomCompatibleExitStatus(status syscall.WaitStatus) bool {
	if status.Signaled() && status.Signal() == syscall.SIGKILL { //nolint:misspell // Signaled is syscall's method name.
		return true
	}

	return status.Exited() &&
		status.ExitStatus() == shellSignalExitCodeOffset+int(syscall.SIGKILL)
}

func schedulerNeedsSigkillMemoryFallback(scheduler string) bool {
	return scheduler == "" || scheduler == "local" || scheduler == "openstack"
}

func sigkillMemoryFallback(status syscall.WaitStatus, peakRAM, requiredRAM int) bool {
	return oomCompatibleExitStatus(status) &&
		peakRAM >= requiredRAM
}
