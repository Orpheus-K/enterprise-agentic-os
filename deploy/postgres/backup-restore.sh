#!/usr/bin/env bash
# Logical backup and verified restore of one AgentOS PostgreSQL database.
#   backup-restore.sh backup  <output-dir>     needs AGENTOS_BACKUP_DATABASE_URL  (superuser URL of the application database)
#   backup-restore.sh restore <dump-file>      needs AGENTOS_RESTORE_DATABASE_URL (superuser URL of an EMPTY database)
# Stop every AgentOS instance before `backup`: the manifest facts must describe the same state as the dump.
# Cluster roles (agentos_migrator/agentos_runtime and their logins) and the pgvector package are not part of a
# dump; create them first as described in README.md. Credentials are read from the URLs and never printed.
set -euo pipefail

# Query failures must propagate explicitly: command substitutions and functions
# called in conditions do not reliably inherit bash errexit.
facts() {
  local url="$1" t value tables
  printf 'manifest_version=2\n'
  value="$(psql "$url" -X --no-password -v ON_ERROR_STOP=1 -Atc "SELECT max(version_id) FROM public.goose_db_version WHERE is_applied")" || return 1
  printf 'schema_version=%s\n' "$value"
  value="$(psql "$url" -X --no-password -v ON_ERROR_STOP=1 -Atc "SELECT extversion FROM pg_extension WHERE extname='vector'")" || return 1
  printf 'pgvector=%s\n' "$value"
  tables="$(psql "$url" -X --no-password -v ON_ERROR_STOP=1 -Atc "SELECT format('%I.%I',n.nspname,c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('iam','channel','knowledge') AND c.relkind='r' ORDER BY n.nspname COLLATE \"C\",c.relname COLLATE \"C\"")" || return 1
  [ -n "$tables" ] || return 1
  while IFS= read -r t; do
    value="$(psql "$url" -X --no-password -v ON_ERROR_STOP=1 -Atc "SELECT pg_get_userbyid(relowner)||','||relrowsecurity||','||relforcerowsecurity FROM pg_class WHERE oid='$t'::regclass")" || return 1
    printf 'table.%s=%s\n' "$t" "$value"
    value="$(psql "$url" -X --no-password -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM $t")" || return 1
    printf 'rows.%s=%s\n' "$t" "$value"
    # Stable text ordering hashes every value, including same-row-count changes
    # to ACLs, pending external intents, checkpoints and reconciliation receipts.
    value="$(PGTZ=UTC PGDATESTYLE=ISO psql "$url" -X --no-password -v ON_ERROR_STOP=1 -Atc "COPY (SELECT row_to_json(t)::text AS row FROM $t t ORDER BY row_to_json(t)::text COLLATE \"C\") TO STDOUT" | sha256sum)" || return 1
    printf 'sha256.%s=%s\n' "$t" "${value%% *}"
  done <<<"$tables"
}

backup() {
  : "${AGENTOS_BACKUP_DATABASE_URL:?set AGENTOS_BACKUP_DATABASE_URL}"
  local out="${1:?output directory}" stamp dump actual
  umask 077
  mkdir -p "$out"
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  dump="$(mktemp "$out/agentos-$stamp-XXXXXX.dump")"
  facts "$AGENTOS_BACKUP_DATABASE_URL" >"$dump.manifest" || { rm -f "$dump" "$dump.manifest"; return 1; }
  pg_dump --format=custom --no-password --dbname="$AGENTOS_BACKUP_DATABASE_URL" --file="$dump" || { rm -f "$dump" "$dump.manifest"; return 1; }
  (cd "$out" && sha256sum "$(basename "$dump")" "$(basename "$dump").manifest" >"$(basename "$dump").sha256")
  # Writers must have been stopped; changed facts means the manifest does not describe the dump.
  if ! actual="$(facts "$AGENTOS_BACKUP_DATABASE_URL")" || ! diff <(printf '%s\n' "$actual") "$dump.manifest" >/dev/null; then
    rm -f "$dump" "$dump.manifest" "$dump.sha256"
    echo "database changed during backup; stop AgentOS instances and retry" >&2
    exit 1
  fi
  echo "$dump"
}

restore() {
  : "${AGENTOS_RESTORE_DATABASE_URL:?set AGENTOS_RESTORE_DATABASE_URL}"
  local dump="${1:?dump file}" url="$AGENTOS_RESTORE_DATABASE_URL" objects actual
  [ -f "$dump" ] && [ -f "$dump.manifest" ] && [ -f "$dump.sha256" ] || { echo "dump, manifest and checksum are required" >&2; exit 1; }
  (cd "$(dirname "$dump")" && sha256sum "$(basename "$dump")" "$(basename "$dump").manifest" | diff - "$(basename "$dump").sha256")
  [ "$(head -n 1 "$dump.manifest")" = "manifest_version=2" ] || { echo "unsupported backup manifest version" >&2; exit 1; }
  objects="$(psql "$url" -X --no-password -v ON_ERROR_STOP=1 -Atc "SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('iam','channel','knowledge','public') AND c.relkind IN ('r','v','m','S')) + (SELECT count(*) FROM pg_extension WHERE extname='vector')")"
  [ "$objects" = "0" ] || { echo "restore target is not empty; refusing to overwrite" >&2; exit 1; }
  pg_restore --exit-on-error --no-password --dbname="$url" "$dump"
  if ! actual="$(facts "$url")" || ! diff <(printf '%s\n' "$actual") "$dump.manifest"; then
    echo "restored database does not match the backup manifest" >&2
    exit 1
  fi
  echo "restore verified: $(basename "$dump")"
}

case "${1:-}" in
  backup) shift; backup "$@" ;;
  restore) shift; restore "$@" ;;
  *) echo "usage: $0 backup <output-dir> | restore <dump-file>" >&2; exit 2 ;;
esac
