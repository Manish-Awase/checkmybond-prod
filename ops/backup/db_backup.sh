#!/usr/bin/env bash
# ops/backup/db_backup.sh
#
# Monthly-organised PostgreSQL backup for CheckMyBond.
#
#   Layout:  $BACKUP_DIR/2026-11/checkmybond_2026-11-21_020000.sql.gz
#
#   BACKUP_STORAGE=local  (default)  backups stay on the server. You copy old
#                         months to your laptop with pull_backups.sh.
#   BACKUP_STORAGE=s3                every backup is also uploaded to S3, verified,
#                         and removed from the server -> server stays clean.
#
#   Alerts / limits (all env variables, see backup.env.example):
#     BACKUP_ALERT_MB   early warning email                  (default 100)
#     BACKUP_MAX_MB     limit: "limit reached" email + auto-delete   (default 500)
#     BACKUP_GRACE_MONTHS  local mode: backups from the current month and the
#                       previous N months are NEVER auto-deleted     (default 3)
#   Anything that needs your attention also sends a healthchecks.io /fail ping.
set -uo pipefail

# ── Load env file if present (so cron needs no exports) ─────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${BACKUP_ENV_FILE:-$SCRIPT_DIR/backup.env}"
if [[ -f "$ENV_FILE" ]]; then set -a; . "$ENV_FILE"; set +a; fi

# ── Settings ────────────────────────────────────────────────────────────────
BACKUP_DIR="${BACKUP_DIR:-/var/backups/checkmybond}"
DB_NAME_PREFIX="${BACKUP_PREFIX:-checkmybond}"
BACKUP_STORAGE="${BACKUP_STORAGE:-local}"           # local | s3
BACKUP_ALERT_MB="${BACKUP_ALERT_MB:-100}"
BACKUP_MAX_MB="${BACKUP_MAX_MB:-500}"               # try 800 / 1000
BACKUP_GRACE_MONTHS="${BACKUP_GRACE_MONTHS:-3}"
ALERT_COOLDOWN_HOURS="${ALERT_COOLDOWN_HOURS:-23}"
HC_PING_URL="${HC_PING_URL:-}"
ALERT_EMAIL_TO="${BACKUP_ALERT_EMAIL_TO:-}"
PG_DOCKER_CONTAINER="${PG_DOCKER_CONTAINER:-}"
APP_NAME="${APP_NAME:-CheckMyBond}"
HOST_LABEL="$(hostname 2>/dev/null || echo server)"

# S3 (only used when BACKUP_STORAGE=s3). Works with AWS and S3-compatible services.
S3_BUCKET="${S3_BUCKET:-}"
S3_PREFIX="${S3_PREFIX:-checkmybond-backups}"
S3_REGION="${S3_REGION:-}"
S3_ENDPOINT_URL="${S3_ENDPOINT_URL:-}"              # only for non-AWS (Wasabi, MinIO, R2...)
S3_STORAGE_CLASS="${S3_STORAGE_CLASS:-}"            # e.g. STANDARD_IA
S3_KEEP_LOCAL_DAYS="${S3_KEEP_LOCAL_DAYS:-7}"       # 0 = remove from server right after upload

mkdir -p "$BACKUP_DIR"
LOG_FILE="$BACKUP_DIR/backup.log"
LOCK_FILE="$BACKUP_DIR/.backup.lock"
log() { echo "$(date -u +'%Y-%m-%dT%H:%M:%SZ') $*" | tee -a "$LOG_FILE" >&2; }

# ── Validate settings ───────────────────────────────────────────────────────
for v in BACKUP_ALERT_MB BACKUP_MAX_MB BACKUP_GRACE_MONTHS ALERT_COOLDOWN_HOURS S3_KEEP_LOCAL_DAYS; do
  [[ "${!v}" =~ ^[0-9]+$ ]] || { echo "ERROR: $v must be a whole number (got '${!v}')" >&2; exit 2; }
done
(( BACKUP_ALERT_MB < BACKUP_MAX_MB )) || { echo "ERROR: BACKUP_ALERT_MB ($BACKUP_ALERT_MB) must be smaller than BACKUP_MAX_MB ($BACKUP_MAX_MB)" >&2; exit 2; }
case "$BACKUP_STORAGE" in
  local) ;;
  s3)
    [[ -n "$S3_BUCKET" ]] || { echo "ERROR: BACKUP_STORAGE=s3 needs S3_BUCKET" >&2; exit 2; }
    command -v aws >/dev/null || { echo "ERROR: BACKUP_STORAGE=s3 needs the AWS CLI ('aws') installed" >&2; exit 2; } ;;
  *) echo "ERROR: BACKUP_STORAGE must be 'local' or 's3' (got '$BACKUP_STORAGE')" >&2; exit 2 ;;
esac
S3_PREFIX="${S3_PREFIX#/}"; S3_PREFIX="${S3_PREFIX%/}"
[[ -n "$S3_REGION" ]] && export AWS_DEFAULT_REGION="$S3_REGION"
AWS_ARGS=(); [[ -n "$S3_ENDPOINT_URL" ]] && AWS_ARGS+=(--endpoint-url "$S3_ENDPOINT_URL")
CP_ARGS=();  [[ -n "$S3_STORAGE_CLASS" ]] && CP_ARGS+=(--storage-class "$S3_STORAGE_CLASS")

# ── healthchecks.io ─────────────────────────────────────────────────────────
hc() {   # hc <"" | start | fail> [body]
  [[ -z "$HC_PING_URL" ]] && return 0
  local suffix="${1:-}" body="${2:-}"
  curl -fsS -m 15 --retry 3 --retry-delay 2 -o /dev/null \
       --data-raw "${body:0:9000}" "${HC_PING_URL%/}${suffix:+/$suffix}" \
    || log "WARN: healthchecks.io ping '${suffix:-success}' failed"
}

# ── Email alert (reuses the app's EMAIL_PROVIDER settings) ──────────────────
send_alert() {   # send_alert <subject> <body>
  local subject="$1" body="$2"
  log "ALERT: $subject"
  [[ -z "$ALERT_EMAIL_TO" ]] && { log "WARN: BACKUP_ALERT_EMAIL_TO not set - alert only logged"; return 0; }
  local provider="${ALERT_EMAIL_PROVIDER:-${EMAIL_PROVIDER:-stdout}}"
  case "$provider" in
    brevo)
      local to_json
      to_json="$(python3 -c 'import json,sys;print(json.dumps([{"email":e.strip()} for e in sys.argv[1].split(",") if e.strip()]))' "$ALERT_EMAIL_TO")"
      python3 - "$subject" "$body" "$to_json" <<'PY' | curl -fsS -m 20 -o /dev/null \
        -H "api-key: ${BREVO_API_KEY:?BREVO_API_KEY not set}" -H "Content-Type: application/json" \
        -d @- https://api.brevo.com/v3/smtp/email || log "WARN: Brevo send failed"
import json,os,sys
print(json.dumps({"sender":{"email":os.environ.get("BREVO_FROM_EMAIL",""),"name":os.environ.get("EMAIL_FROM_NAME","CheckMyBond")},
  "to":json.loads(sys.argv[3]),"subject":sys.argv[1],"textContent":sys.argv[2]}))
PY
      ;;
    sendgrid)
      python3 - "$subject" "$body" "$ALERT_EMAIL_TO" <<'PY' | curl -fsS -m 20 -o /dev/null \
        -H "Authorization: Bearer ${SENDGRID_API_KEY:?SENDGRID_API_KEY not set}" -H "Content-Type: application/json" \
        -d @- https://api.sendgrid.com/v3/mail/send || log "WARN: SendGrid send failed"
import json,os,sys
print(json.dumps({"personalizations":[{"to":[{"email":e.strip()} for e in sys.argv[3].split(",") if e.strip()]}],
  "from":{"email":os.environ.get("SENDGRID_FROM_EMAIL","")},"subject":sys.argv[1],
  "content":[{"type":"text/plain","value":sys.argv[2]}]}))
PY
      ;;
    smtp)
      local from="${SMTP_FROM_EMAIL:?SMTP_FROM_EMAIL not set}" rcpt=() r _r
      IFS=',' read -ra _r <<< "$ALERT_EMAIL_TO"
      for r in "${_r[@]}"; do rcpt+=(--mail-rcpt "$(echo "$r" | xargs)"); done
      printf 'From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n' "$from" "$ALERT_EMAIL_TO" "$subject" "$body" \
        | curl -fsS -m 30 --ssl-reqd --url "smtp://${SMTP_HOST:?}:${SMTP_PORT:-587}" \
            --user "${SMTP_USERNAME:-}:${SMTP_PASSWORD:-}" --mail-from "$from" "${rcpt[@]}" -T - \
        || log "WARN: SMTP send failed"
      ;;
    *) log "Email provider '$provider' = log only. Message: $body" ;;
  esac
}

# Rate limit per alert kind: cooldown_ok <kind>  (returns 0 if allowed; records the time)
cooldown_ok() {
  local f="$BACKUP_DIR/.last_alert_$1" now last
  now="$(date +%s)"; last="$(cat "$f" 2>/dev/null || echo 0)"
  (( now - last >= ALERT_COOLDOWN_HOURS * 3600 )) || return 1
  echo "$now" > "$f"; return 0
}

# ── Local file helpers ──────────────────────────────────────────────────────
backup_files_oldest_first() {
  find "$BACKUP_DIR" -mindepth 2 -maxdepth 2 -type f -name "${DB_NAME_PREFIX}_*.sql.gz" | sort
}
total_mb() {
  find "$BACKUP_DIR" -mindepth 2 -maxdepth 2 -type f -name "${DB_NAME_PREFIX}_*.sql.gz" -printf '%s\n' \
    | awk '{s+=$1} END{printf "%d", s/1024/1024}'
}
month_summary() {
  du -sm --apparent-size "$BACKUP_DIR"/20??-?? 2>/dev/null | awk '{printf "  %s  %s MB\n",$2,$1}' | sed "s#$BACKUP_DIR/##"
}
remove_file() { rm -f -- "$1" && { rmdir --ignore-fail-on-non-empty "$(dirname "$1")" 2>/dev/null; return 0; }; return 1; }

# Oldest month that is protected from auto-delete in local mode.
CUTOFF_MONTH="$(date -d "$(date +%Y-%m-01) -${BACKUP_GRACE_MONTHS} months" +%Y-%m)"

# ── S3 helpers ──────────────────────────────────────────────────────────────
s3_key() { echo "${S3_PREFIX:+$S3_PREFIX/}$(basename "$(dirname "$1")")/$(basename "$1")"; }
s3_has() {   # file exists in S3 with identical size?
  local remote
  remote="$(aws ${AWS_ARGS[@]+"${AWS_ARGS[@]}"} s3api head-object --bucket "$S3_BUCKET" \
            --key "$(s3_key "$1")" --query ContentLength --output text 2>/dev/null)" || return 1
  [[ "$remote" == "$(stat -c %s "$1")" ]]
}
s3_upload() {
  aws ${AWS_ARGS[@]+"${AWS_ARGS[@]}"} s3 cp "$1" "s3://$S3_BUCKET/$(s3_key "$1")" --only-show-errors \
      ${CP_ARGS[@]+"${CP_ARGS[@]}"}
}

# May this file be removed by the AUTO-DELETE step?
is_auto_deletable() {
  local f="$1" month; month="$(basename "$(dirname "$f")")"
  if [[ "$BACKUP_STORAGE" == "s3" ]]; then
    s3_has "$f"                      # only if a verified copy is in S3
  else
    [[ "$month" < "$CUTOFF_MONTH" ]] # only months older than the grace window
  fi
}

PROBLEMS=()

# ── Single instance only ────────────────────────────────────────────────────
exec 9>"$LOCK_FILE"
flock -n 9 || { log "Another backup is already running - exiting"; exit 0; }

hc start
log "=== Backup started ($HOST_LABEL, storage=$BACKUP_STORAGE) ==="

# ── 1. Dump ─────────────────────────────────────────────────────────────────
MONTH_DIR="$BACKUP_DIR/$(date +%Y-%m)"
mkdir -p "$MONTH_DIR"
FINAL="$MONTH_DIR/${DB_NAME_PREFIX}_$(date +%Y-%m-%d_%H%M%S).sql.gz"
TMP="$FINAL.partial"

if [[ -n "$PG_DOCKER_CONTAINER" ]]; then
  docker exec -i "$PG_DOCKER_CONTAINER" pg_dump -U "${PG_DOCKER_USER:-postgres}" \
      -d "${PG_DOCKER_DB:-$DB_NAME_PREFIX}" --no-owner --no-privileges 2>>"$LOG_FILE" | gzip -9 > "$TMP"
  DUMP_RC=${PIPESTATUS[0]}
else
  : "${DATABASE_URL:?DATABASE_URL not set (or set PG_DOCKER_CONTAINER)}"
  PG_URL="$(echo "$DATABASE_URL" | sed -E 's#^(postgres(ql)?)\+[a-z0-9]+://#\1://#')"   # strip SQLAlchemy "+driver"
  pg_dump "$PG_URL" --no-owner --no-privileges 2>>"$LOG_FILE" | gzip -9 > "$TMP"
  DUMP_RC=${PIPESTATUS[0]}
fi

if (( DUMP_RC != 0 )) || ! gzip -t "$TMP" 2>/dev/null || [[ "$(stat -c %s "$TMP")" -lt 200 ]]; then
  rm -f "$TMP"
  log "ERROR: pg_dump failed (exit $DUMP_RC)"
  hc fail "pg_dump failed on $HOST_LABEL (exit $DUMP_RC). See $LOG_FILE"
  exit 1
fi
mv "$TMP" "$FINAL"
log "Backup written: $FINAL ($(( $(stat -c %s "$FINAL") / 1024 )) KB)"

# ── 2. S3 mode: upload everything not yet in S3, then clean the server ──────
if [[ "$BACKUP_STORAGE" == "s3" ]]; then
  UPLOADED=0; UP_FAILED=0
  while IFS= read -r f; do
    s3_has "$f" && continue
    if s3_upload "$f" && s3_has "$f"; then
      log "Uploaded to S3: s3://$S3_BUCKET/$(s3_key "$f")"; UPLOADED=$((UPLOADED+1))
    else
      log "ERROR: S3 upload/verify failed for $f"; UP_FAILED=$((UP_FAILED+1))
    fi
  done < <(backup_files_oldest_first)
  (( UP_FAILED > 0 )) && PROBLEMS+=("S3 upload failed for $UP_FAILED file(s) - they are kept on the server and retried next run.")

  # Remove server copies that are verified in S3 and older than S3_KEEP_LOCAL_DAYS
  NOW="$(date +%s)"
  while IFS= read -r f; do
    age_days=$(( (NOW - $(stat -c %Y "$f")) / 86400 ))
    (( age_days >= S3_KEEP_LOCAL_DAYS )) || continue
    if [[ "$S3_KEEP_LOCAL_DAYS" -gt 0 && "$f" == "$FINAL" ]]; then continue; fi
    if s3_has "$f" && remove_file "$f"; then log "Removed server copy (safe in S3): $f"; fi
  done < <(backup_files_oldest_first)
fi

# ── 3. Size check, limit alert, auto-delete ─────────────────────────────────
USED="$(total_mb)"
LIMIT_ALERT_SENT=0
if (( USED > BACKUP_MAX_MB )); then
  log "Storage ${USED} MB is over the limit of ${BACKUP_MAX_MB} MB"
  USED_BEFORE="$USED"; DELETED=()
  while (( USED > BACKUP_MAX_MB )); do
    VICTIM=""
    while IFS= read -r f; do
      [[ "$f" == "$FINAL" ]] && continue          # never delete the newest backup
      if is_auto_deletable "$f"; then VICTIM="$f"; break; fi
    done < <(backup_files_oldest_first)
    [[ -z "$VICTIM" ]] && break                     # nothing left that is allowed to go
    if remove_file "$VICTIM"; then log "Auto-deleted: $VICTIM"; DELETED+=("$(basename "$VICTIM")")
    else log "ERROR: could not delete $VICTIM"; break; fi
    USED="$(total_mb)"
  done

  if [[ "$BACKUP_STORAGE" == "s3" ]]; then WHY="only files already verified in S3 may be auto-deleted"
  else WHY="backups from ${CUTOFF_MONTH} onward (current month + previous ${BACKUP_GRACE_MONTHS}) are protected"; fi

  if cooldown_ok limit; then
    send_alert "[$APP_NAME] Backup storage LIMIT reached (${USED_BEFORE} MB / ${BACKUP_MAX_MB} MB)" \
"The backup folder on $HOST_LABEL went over its ${BACKUP_MAX_MB} MB limit (it was ${USED_BEFORE} MB).

Auto-deleted ${#DELETED[@]} old backup file(s); now ${USED} MB. Rule: ${WHY}.

Per month now:
$(month_summary)

$( (( USED > BACKUP_MAX_MB )) && echo "STILL OVER THE LIMIT - nothing more may be auto-deleted. Copy old months to your laptop with pull_backups.sh --delete-remote, raise BACKUP_MAX_MB, or switch on S3 (BACKUP_STORAGE=s3)." || echo "Back under the limit. Copy old months to your laptop soon so auto-delete does not have to remove them.")"
  fi
  LIMIT_ALERT_SENT=1

  if (( USED > BACKUP_MAX_MB )); then
    PROBLEMS+=("AUTO-DELETE COULD NOT FREE SPACE on $HOST_LABEL: ${USED} MB used, limit ${BACKUP_MAX_MB} MB ($WHY). Move old months off the server or raise BACKUP_MAX_MB.")
  fi
fi

# ── 4. Early warning at the alert threshold (skipped if limit mail just went) ─
if (( LIMIT_ALERT_SENT == 0 && USED >= BACKUP_ALERT_MB )) && cooldown_ok warn; then
  send_alert "[$APP_NAME] Backup storage at ${USED} MB (alert level ${BACKUP_ALERT_MB} MB)" \
"Database backups on $HOST_LABEL now use ${USED} MB (alert level ${BACKUP_ALERT_MB} MB, limit ${BACKUP_MAX_MB} MB).

Per month:
$(month_summary)

Copy finished months to your laptop (pull_backups.sh --delete-remote) to free space."
fi

# ── 5. Report ───────────────────────────────────────────────────────────────
if (( ${#PROBLEMS[@]} > 0 )); then
  MSG="$(printf '%s ' "${PROBLEMS[@]}")"
  log "ERROR: $MSG"
  hc fail "$MSG"
  exit 1
fi
log "=== Backup OK - using ${USED} MB of ${BACKUP_MAX_MB} MB ==="
hc "" "Backup OK on $HOST_LABEL - ${USED}/${BACKUP_MAX_MB} MB used (storage=$BACKUP_STORAGE)"
exit 0
