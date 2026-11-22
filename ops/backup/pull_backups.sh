#!/usr/bin/env bash
# ops/backup/pull_backups.sh  —  run on YOUR LAPTOP (macOS / Linux / WSL / Git Bash)
#
# Copies every COMPLETED month (everything except the server's current month) from
# the server to your laptop, verifies the copy, and — only if you ask — deletes
# those months from the server to free space.
#
#   ./pull_backups.sh                    copy + verify only (safe, deletes nothing)
#   ./pull_backups.sh --delete-remote    copy + verify, then ask before deleting on server
#   ./pull_backups.sh --delete-remote --yes   same but no question (for scripts)
#
# Settings (env vars, or put them in ~/.checkmybond-pull.env):
#   SERVER             user@host                         (required)
#   SSH_PORT           22
#   REMOTE_BACKUP_DIR  /var/backups/checkmybond
#   LOCAL_BACKUP_DIR   $HOME/checkmybond-backups
set -uo pipefail
[[ -f "$HOME/.checkmybond-pull.env" ]] && { set -a; . "$HOME/.checkmybond-pull.env"; set +a; }

SERVER="${SERVER:-}"
SSH_PORT="${SSH_PORT:-22}"
REMOTE_BACKUP_DIR="${REMOTE_BACKUP_DIR:-/var/backups/checkmybond}"
LOCAL_BACKUP_DIR="${LOCAL_BACKUP_DIR:-$HOME/checkmybond-backups}"
DELETE_REMOTE=0; ASSUME_YES=0
for a in "$@"; do
  case "$a" in
    --delete-remote) DELETE_REMOTE=1 ;;
    --yes) ASSUME_YES=1 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "Unknown option: $a" >&2; exit 2 ;;
  esac
done
[[ -n "$SERVER" ]] || { echo "Set SERVER=user@host (env var or ~/.checkmybond-pull.env)" >&2; exit 2; }
[[ "$REMOTE_BACKUP_DIR" == /* && "$REMOTE_BACKUP_DIR" != "/" ]] || { echo "REMOTE_BACKUP_DIR must be an absolute path" >&2; exit 2; }
command -v rsync >/dev/null || { echo "rsync is required on your laptop" >&2; exit 2; }

SSH=(ssh -p "$SSH_PORT")
RSYNC_SSH="ssh -p $SSH_PORT"
mkdir -p "$LOCAL_BACKUP_DIR"

CURRENT_MONTH="$("${SSH[@]}" "$SERVER" 'date +%Y-%m')" || { echo "Cannot reach $SERVER" >&2; exit 1; }
[[ "$CURRENT_MONTH" =~ ^[0-9]{4}-[0-9]{2}$ ]] || { echo "Unexpected date from server: $CURRENT_MONTH" >&2; exit 1; }

# Completed months = YYYY-MM folders on the server, except the current one
mapfile -t MONTHS < <("${SSH[@]}" "$SERVER" "ls -1 '$REMOTE_BACKUP_DIR'" | grep -E '^[0-9]{4}-[0-9]{2}$' | grep -vx "$CURRENT_MONTH" | sort)
if (( ${#MONTHS[@]} == 0 )); then echo "No completed months on the server (current month $CURRENT_MONTH is never touched). Nothing to do."; exit 0; fi
echo "Server current month: $CURRENT_MONTH (not touched)"
echo "Completed months to copy: ${MONTHS[*]}"
echo

VERIFIED=()
for m in "${MONTHS[@]}"; do
  echo "── $m: copying..."
  mkdir -p "$LOCAL_BACKUP_DIR/$m"
  if ! rsync -a --partial -e "$RSYNC_SSH" "$SERVER:$REMOTE_BACKUP_DIR/$m/" "$LOCAL_BACKUP_DIR/$m/"; then
    echo "   ✗ copy failed - skipping $m"; continue
  fi
  # Verify 1: a checksum comparison must find ZERO differences
  DIFF="$(rsync -rn --checksum --itemize-changes -e "$RSYNC_SSH" "$SERVER:$REMOTE_BACKUP_DIR/$m/" "$LOCAL_BACKUP_DIR/$m/")"
  # Verify 2: every gzip must be readable
  BAD=0; for f in "$LOCAL_BACKUP_DIR/$m"/*.sql.gz; do [[ -e "$f" ]] && ! gzip -t "$f" 2>/dev/null && { echo "   ✗ corrupt: $f"; BAD=1; }; done
  if [[ -z "$DIFF" && $BAD -eq 0 ]]; then
    echo "   ✓ verified ($(ls -1 "$LOCAL_BACKUP_DIR/$m"/*.sql.gz 2>/dev/null | wc -l | xargs) files, $(du -sh "$LOCAL_BACKUP_DIR/$m" | cut -f1))"
    VERIFIED+=("$m")
  else
    echo "   ✗ verification FAILED for $m - will not delete it from the server"; [[ -n "$DIFF" ]] && echo "$DIFF" | head -5
  fi
done

echo
echo "Copied & verified in $LOCAL_BACKUP_DIR: ${VERIFIED[*]:-none}"
(( DELETE_REMOTE )) || { echo "Nothing deleted on the server. Re-run with --delete-remote to free server space."; exit 0; }
(( ${#VERIFIED[@]} )) || { echo "Nothing verified, so nothing to delete."; exit 1; }

echo
echo "About to DELETE from $SERVER:$REMOTE_BACKUP_DIR  ->  ${VERIFIED[*]}"
if (( ! ASSUME_YES )); then
  read -r -p "Type DELETE to confirm: " ans; [[ "$ans" == "DELETE" ]] || { echo "Cancelled. Nothing deleted."; exit 0; }
fi
for m in "${VERIFIED[@]}"; do
  [[ "$m" =~ ^[0-9]{4}-[0-9]{2}$ && "$m" != "$CURRENT_MONTH" ]] || { echo "Refusing to delete '$m'"; continue; }
  "${SSH[@]}" "$SERVER" "rm -rf -- '$REMOTE_BACKUP_DIR/$m'" && echo "   deleted on server: $m"
done
echo "Done. Keep $LOCAL_BACKUP_DIR safe - it now holds the only copy of those months."
