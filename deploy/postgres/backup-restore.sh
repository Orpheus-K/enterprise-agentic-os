#!/usr/bin/env bash
# Logical backup and verified restore of one AgentOS PostgreSQL database.
#   backup-restore.sh backup  <output-dir>     needs AGENTOS_BACKUP_DATABASE_URL  (superuser URL of the application database)
#   backup-restore.sh restore <dump-file>      needs AGENTOS_RESTORE_DATABASE_URL (superuser URL of an EMPTY database)
# Stop every AgentOS instance before `backup`: the manifest counts must describe the same state as the dump.
# Cluster roles (agentos_migrator/agentos_runtime and their logins) and the pgvector package are not part of a
# dump; create them first as described in README.md. Credentials are read from the URLs and never printed.
set -euo pipefail

tables="iam.tenants iam.members iam.identity_links channel.tasks channel.audit channel.approvals knowledge.documents knowledge.chunks knowledge.chunk_vectors"

# facts prints facts that must be identical before backup and after restore.
facts() {
  local url="$1" t
  echo "schema_version=$(psql "$url" -v ON_ERROR_STOP=1 -Atc "SELECT max(version_id) FROM public.goose_db_version WHERE is_applied")"
  echo "pgvector=$(psql "$url" -v ON_ERROR_STOP=1 -Atc "SELECT extversion FROM pg_extension WHERE extname='vector'")"
  echo "forced_rls=$(psql "$url" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('iam','channel','knowledge') AND c.relkind='r' AND c.relrowsecurity AND c.relforcerowsecurity")"
  echo "owner_roles=$(psql "$url" -v ON_ERROR_STOP=1 -Atc "SELECT string_agg(DISTINCT pg_get_userbyid(c.relowner), ',' ORDER BY pg_get_userbyid(c.relowner)) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('iam','channel','knowledge') AND c.relkind='r'")"
  for t in $tables; do
    echo "rows.$t=$(psql "$url" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM $t")"
  done
}

backup() {
  : "${AGENTOS_BACKUP_DATABASE_URL:?set AGENTOS_BACKUP_DATABASE_URL}"
  local out="${1:?output directory}" stamp dump
  umask 077
  mkdir -p "$out"
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  dump="$out/agentos-$stamp.dump"
  facts "$AGENTOS_BACKUP_DATABASE_URL" >"$dump.manifest"
  pg_dump --format=custom --no-password --dbname="$AGENTOS_BACKUP_DATABASE_URL" --file="$dump"
  (cd "$out" && sha256sum "$(basename "$dump")" >"$(basename "$dump").sha256")
  # Writers must have been stopped; a drifting count means the manifest does not describe the dump.
  if ! diff <(facts "$AGENTOS_BACKUP_DATABASE_URL") "$dump.manifest" >/dev/null; then
    rm -f "$dump" "$dump.manifest" "$dump.sha256"
    echo "database changed during backup; stop AgentOS instances and retry" >&2
    exit 1
  fi
  echo "$dump"
}

restore() {
  : "${AGENTOS_RESTORE_DATABASE_URL:?set AGENTOS_RESTORE_DATABASE_URL}"
  local dump="${1:?dump file}" url="$AGENTOS_RESTORE_DATABASE_URL" objects
  [ -f "$dump" ] && [ -f "$dump.manifest" ] && [ -f "$dump.sha256" ] || { echo "dump, manifest and checksum are required" >&2; exit 1; }
  (cd "$(dirname "$dump")" && sha256sum --check --strict "$(basename "$dump").sha256")
  objects="$(psql "$url" -v ON_ERROR_STOP=1 -Atc "SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('iam','channel','knowledge','public') AND c.relkind IN ('r','v','m','S')) + (SELECT count(*) FROM pg_extension WHERE extname='vector')")"
  [ "$objects" = "0" ] || { echo "restore target is not empty; refusing to overwrite" >&2; exit 1; }
  pg_restore --exit-on-error --no-password --dbname="$url" "$dump"
  if ! diff <(facts "$url") "$dump.manifest"; then
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
