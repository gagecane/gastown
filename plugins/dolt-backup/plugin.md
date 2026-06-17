+++
name = "dolt-backup"
description = "Smart Dolt database backup with change detection"
version = 2

[gate]
type = "cooldown"
duration = "15m"

[tracking]
labels = ["plugin:dolt-backup", "category:data-safety"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "high"
+++

# Dolt Backup

Syncs production Dolt databases to filesystem backups via `dolt backup sync`.
Executed via `run.sh` — no AI interpretation.

## What it does

1. For each production DB (hq, beads, gt): compare HEAD hash against last backup
2. Skip unchanged databases
3. Run `dolt backup sync` for changed databases
4. Self-heal a corrupt backup dest: if a sync fails because the dest manifest
   references a missing table file ("table file not found" — gu-90fcw), discard
   that dest and re-initialize it with a full fresh sync from the intact live
   source. Repair touches only the backup dest, never the live DB. Counts as
   `synced` + `repaired` on success; falls through to `FAILED` if unrecoverable.
5. Only escalate when actual backup operations fail (FAILED > 0)

## Usage

```bash
./run.sh                          # Normal execution
./run.sh --dry-run                # Report without syncing
./run.sh --databases hq,beads    # Specific databases only
DOLT_BACKUP_AUTO_REPAIR=0 ./run.sh  # Disable corrupt-dest self-heal
```
