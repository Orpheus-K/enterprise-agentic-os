#!/usr/bin/env bash
# No database needed: a failed facts query must never reach pg_dump.
set -euo pipefail
script="${1:-$(dirname "$0")/backup-restore.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"
cat >"$work/bin/psql" <<'SH'
#!/usr/bin/env bash
exit 1
SH
cat >"$work/bin/pg_dump" <<'SH'
#!/usr/bin/env bash
touch "$BACKUP_TEST_MARKER"
for arg in "$@"; do
  case "$arg" in --file=*) touch "${arg#--file=}" ;; esac
done
SH
chmod +x "$work/bin/"*
export BACKUP_TEST_MARKER="$work/dump-called"
if PATH="$work/bin:$PATH" AGENTOS_BACKUP_DATABASE_URL=synthetic bash "$script" backup "$work/out" >"$work/log" 2>&1; then
  echo "FAIL: database query failure reported a successful backup" >&2
  exit 1
fi
if [ -e "$BACKUP_TEST_MARKER" ] || [ -n "$(find "$work/out" -type f -print -quit)" ]; then
  echo "FAIL: query failure left a backup artifact or reached pg_dump" >&2
  exit 1
fi
echo "backup facts query failure rejected"
