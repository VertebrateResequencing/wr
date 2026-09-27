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

// This file contains the in-memory index that lets a command dependency, which
// names only a Cmd and maybe a Cwd, find the live jobs running that command
// with mounts or a container image.

import (
	"maps"
	"slices"
	"sync"
)

// variantJobShard holds the command keys of the indexed job keys that hash to
// it.
type variantJobShard struct {
	mu      sync.Mutex
	command map[string]string
}

// commandVariants indexes the live jobs whose key is not their command key (see
// Job.commandKey) - those with MountConfigs or a container image - by that
// command key. A job whose key is its command key is not held, since a lookup
// of the command key itself already finds it; that keeps the index's size to
// the live jobs that use mounts or containers.
//
// It is kept alongside, and maintained at exactly the same points as, the dep
// group membership in depGroupMembers. Like that state it can briefly hold a
// key that has just left the live bucket, so a caller must still check each key
// it returns is live.
//
// Both maps are sharded, as depGroupMembers' are, so the add, archive, delete
// and modify paths never contend on one server-wide lock (DEVELOPERS.md rule
// 2). A job-key shard is taken before a command-key shard, never the reverse,
// and never 2 shards of the same map at once.
type commandVariants struct {
	byCommand [depGroupShards]depGroupShard
	byJob     [depGroupShards]variantJobShard
}

func (v *commandVariants) init() {
	for i := range depGroupShards {
		v.byCommand[i].members = make(map[string]map[string]bool)
		v.byJob[i].command = make(map[string]string)
	}
}

// record indexes jobKey under commandKey. Idempotent: a job key is a hash of
// the job's Cmd, Cwd and the rest, so it always has the same command key. A job
// whose key is its command key is not indexed.
func (v *commandVariants) record(jobKey, commandKey string) {
	if jobKey == commandKey {
		return
	}

	shard := &v.byJob[depGroupShardIndex(jobKey)]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if _, held := shard.command[jobKey]; held {
		return
	}

	shard.command[jobKey] = commandKey

	byCommand := &v.byCommand[depGroupShardIndex(commandKey)]

	byCommand.mu.Lock()
	byCommand.addLocked(commandKey, jobKey)
	byCommand.mu.Unlock()
}

// forget removes jobKey from the index. Idempotent.
func (v *commandVariants) forget(jobKey string) {
	shard := &v.byJob[depGroupShardIndex(jobKey)]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	commandKey, held := shard.command[jobKey]
	if !held {
		return
	}

	delete(shard.command, jobKey)

	byCommand := &v.byCommand[depGroupShardIndex(commandKey)]

	byCommand.mu.Lock()
	byCommand.dropLocked(commandKey, jobKey)
	byCommand.mu.Unlock()
}

// rekey is record across a job key change. newKey is recorded before oldKey is
// forgotten, so a resolution running at the same time sees the job under one
// key or both, never neither.
func (v *commandVariants) rekey(oldKey, newKey, commandKey string) {
	v.record(newKey, commandKey)

	if oldKey != newKey {
		v.forget(oldKey)
	}
}

// of returns the job keys indexed under commandKey, in no particular order.
func (v *commandVariants) of(commandKey string) []string {
	shard := &v.byCommand[depGroupShardIndex(commandKey)]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	return slices.Collect(maps.Keys(shard.members[commandKey]))
}
