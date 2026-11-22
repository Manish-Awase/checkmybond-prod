# ops/backup — monthly database backups

`db_backup.sh` (runs on the **server** via cron) dumps the `checkmybond` Postgres database
into one folder per month:

```
/var/backups/checkmybond/
├── 2026-09/checkmybond_2026-09-01_020000.sql.gz ...
├── 2026-10/...
├── 2026-11/...            <- current month
└── backup.log
```

`pull_backups.sh` (runs on **your laptop**) copies finished months off the server.

## Two ways to store backups — switch with one env variable

| | `BACKUP_STORAGE=local` (default) | `BACKUP_STORAGE=s3` |
|---|---|---|
| Where data lives | Server, then your laptop | S3 (full history); server keeps only the last `S3_KEEP_LOCAL_DAYS` days |
| Freeing server space | You run `pull_backups.sh --delete-remote` | Automatic after each verified upload |
| Auto-delete may remove | Only months **older than the 3-month grace window** | Only files **already verified in S3** |

### Local mode (no cloud)
On your laptop (macOS/Linux/WSL/Git Bash; needs `rsync` + `ssh`):

```bash
export SERVER=ubuntu@your.server.ip          # or put it in ~/.checkmybond-pull.env
./pull_backups.sh                            # copy + verify every finished month. Deletes nothing.
./pull_backups.sh --delete-remote            # ...then asks you to type DELETE to remove them from the server
```
- The server's **current month is never copied or deleted.**
- A month is only deleted from the server if every file passed a checksum comparison and a `gzip -t` test.
- Back up your laptop folder too — after deletion it holds the only copy.

### S3 mode
1. Install the AWS CLI on the server (`sudo apt install awscli`, or the official v2 installer).
2. In `backup.env` fill `S3_BUCKET`, `S3_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and set `BACKUP_STORAGE=s3`.
3. Run `./db_backup.sh` once by hand. All existing local backups are uploaded on that first run, then cleaned from the server per `S3_KEEP_LOCAL_DAYS`.

Every file is uploaded, then its size is checked in S3 (`head-object`) **before** any server copy is removed.
If an upload fails, the file stays on the server, is retried next run, and healthchecks.io gets a `/fail` ping.
The script never deletes anything in S3 — set a bucket lifecycle rule if you want old data to expire or move to a cheaper class.
Use an IAM user limited to `s3:PutObject`, `s3:GetObject` and `s3:ListBucket` on that one bucket/prefix.

## Limits and alerts (env variables)

| Variable | Default | What happens |
|---|---|---|
| `BACKUP_ALERT_MB` | 100 | Early-warning email with per-month sizes |
| `BACKUP_MAX_MB` | 500 | **"Limit reached" email**, then auto-delete. Change to 800/1000 freely |
| `BACKUP_GRACE_MONTHS` | 3 | Local mode: current month + previous 3 months are never auto-deleted |

If the limit is hit and nothing is allowed to be deleted (everything is inside the grace window, or not yet in S3),
the script sends the limit email **and** a healthchecks.io `/fail` ping, and keeps the backups. That gap is your
time to copy months to your laptop, raise `BACKUP_MAX_MB`, or switch on S3. With `BACKUP_MAX_MB=500` you may see this
if a few months of backups are individually large.

The newest backup is never auto-deleted. `BACKUP_ALERT_MB` must stay below `BACKUP_MAX_MB`.

## Server setup

```bash
sudo mkdir -p /opt/checkmybond/ops/backup /var/backups/checkmybond
sudo cp db_backup.sh backup.env.example /opt/checkmybond/ops/backup/
cd /opt/checkmybond/ops/backup
cp backup.env.example backup.env && chmod 600 backup.env && nano backup.env
chmod +x db_backup.sh
sudo apt install postgresql-client curl python3     # skip postgresql-client if using PG_DOCKER_CONTAINER
./db_backup.sh                                      # test once, then check /var/backups/checkmybond/backup.log
```
Cron (daily 02:00) — `crontab -e`:
```
0 2 * * * /opt/checkmybond/ops/backup/db_backup.sh >/dev/null 2>&1
```
Use a `pg_dump` version equal to or newer than your Postgres server.

## healthchecks.io
Create a check, put its URL in `HC_PING_URL`, set **Period = 1 day, Grace = 1 hour**, and add your notification
channel. You then also hear about a dead cron or a down server, which the script can't report itself.

## Restore
```bash
gunzip -c checkmybond_2026-11-21_020000.sql.gz | psql -d checkmybond_restore
# S3: aws s3 cp s3://BUCKET/checkmybond-backups/2026-11/FILE.sql.gz .
```
Restore into a new empty database first and check it before touching the live one.
