# D5: local-disk primary with asynchronous replication to NFS

## Sketch

Put the durable store (any log design; the prototype uses D2's log) on the
manager host's local disk, where fdatasync is not shared with a busy NFS
server or a farm-wide filesystem, and ship it continuously to NFS (or S3) as
the off-host copy. Replication is the backup: log segments and snapshots are
append-only, so shipping is "copy the new bytes", not "copy the whole file".

- Writes: exactly D2's, on local disk.
- Shipper: a goroutine tails each file and appends new bytes to the replica,
  syncing it about once a second (`proto/cmd/hotpath -ship`). Sealed segments
  are shipped once and never touched again.
- Recovery on the same host: from local files. After losing the host: from
  the replica, which may miss up to the replication lag.

## Crash recovery

- Process crash: nothing acknowledged is lost (the local log is synced).
- Host crash with disk intact: nothing acknowledged is lost.
- Host or disk lost: the replica misses the last replication lag (measured
  in `benchmarks.md`). Those records include reservations that were handed
  out, so a restore on a new host can re-run up to a lag's worth of jobs.
  Restoring then needs the D6 quarantine (hold every job that the replica
  shows live until the TTR has passed or its runner has reported) to rule out
  double runs.
- NFS stalls no longer stall the manager: only the shipper waits.

## Group commit and fdatasync

Local fdatasync on this VM disk: 0.6-0.9ms for small appends, similar to an
idle NFS, but it is not shared with the farm, so it does not degrade under
load or stall with the filesystem. On a real local SSD it is 50-200us.

## Deployment reality

wr's manager database lives on NFS in production partly because the manager
host's local disk is not trusted or not large enough, and so that the
manager can be restarted on another host. D5 needs a local disk sized for the
store (the live set plus history segments not yet shipped; history can live
on NFS since it is immutable and only read) and an operator procedure for
restoring on another host. The owner would need to decide whether that is
acceptable.

## Files and inodes

As D2, twice (primary and replica).

## Complexity, risk, how much of wr changes

D2 plus a shipper (about 300 lines) and restore logic that quarantines live
jobs. The new risk is operational: a local disk to provision, and a second
copy that is by design slightly stale.
