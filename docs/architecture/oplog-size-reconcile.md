# Live oplog size reconcile

## Problem

`spec.replsets[].configuration` → `replication.oplogSizeMB` only governs oplog
creation. `mongod` does not resize an existing `oplog.rs` collection on restart,
so changing `oplogSizeMB` restarts the pods but leaves the live oplog at its old
size. Plan changes therefore never take effect on the running oplog.

## Behavior

After a replset's rolling restart completes and the set is stable (all pods
live), the operator reconciles each member's live oplog size to match
`replication.oplogSizeMB`:

- The desired size is parsed from the CR via
  `MongoConfiguration.GetOplogSizeMB()`. A missing/zero value skips the replset.
- `GetOplogSizeMB` reads each member's configured size via `collStats` on
  `local.oplog.rs`. A member within `oplogSizeTolerance` MB of the desired size
  is left untouched, so the reconcile is a no-op once every member matches.
- Members are resized one at a time, secondaries first and the primary last,
  each over its own standalone connection. `replSetResizeOplog` is node-local
  and does not replicate, so every member must be resized individually. `size`
  is sent as a BSON `Double`.
- After each resize the configured size is re-read and validated against the
  desired value, and the member is confirmed healthy (`PRIMARY`/`SECONDARY`,
  health up) before moving to the next. The reconcile returns an error and
  requeues if a member becomes unhealthy.

## Decrease path (gated)

Shrinking the oplog additionally runs `compact: "oplog.rs"` to reclaim disk
space. `compact` blocks replication on the member it runs against, so the
primary is stepped down before its compact. The decrease path is disabled by
default (`allowOplogDecrease`) pending review of the compact impact; the
increase path is the primary deliverable.

## Config-server exclusion

Config-server replica sets (`clusterRole: configsvr`, or the reserved `cfg`
replset name) are excluded from this reconcile by default. Their corrected oplog
value is tracked separately in LIB-1368.
