#!/bin/sh
# Round-trip test of damson-backup.sh and damson-restore.sh. SQLite needs the
# sqlite3 CLI. PostgreSQL runs only with PGTEST_ADMIN_URL set to a superuser URL
# of a scratch server (postgres://user:pw@host:5432/postgres) and needs pg_dump,
# pg_restore and psql, of the same major version as the server.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "backup test: $*" >&2; exit 1; }

# --- SQLite
if command -v sqlite3 >/dev/null 2>&1; then
	src="$tmp/src.db"
	sqlite3 "$src" "CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('hello');"
	mkdir "$tmp/etc"; echo "kek-material" > "$tmp/etc/damson.kek"
	export DAMSON_DATABASE__DRIVER=sqlite DAMSON_DATABASE__DSN="file:$src?_pragma=busy_timeout(5000)"
	"$here/damson-backup.sh" -o "$tmp/out" -f "$tmp/etc" >/dev/null
	b="$(ls -d "$tmp"/out/damson-backup-*)"
	[ -f "$b/database.sqlite" ] && [ -f "$b/files.tar.gz" ] && [ -f "$b/MANIFEST.sha256" ] || fail "sqlite: missing output files in $b"
	[ "$(stat -c %a "$b/database.sqlite")" = 600 ] || fail "sqlite: backup file is not mode 0600"
	dst="$tmp/dst.db"
	DAMSON_DATABASE__DSN="file:$dst" "$here/damson-restore.sh" "$b" >/dev/null
	[ "$(sqlite3 "$dst" 'SELECT v FROM t')" = hello ] || fail "sqlite: restored data differs"
	if DAMSON_DATABASE__DSN="file:$dst" "$here/damson-restore.sh" "$b" >/dev/null 2>&1; then fail "sqlite: restore overwrote an existing file"; fi
	tar -tzf "$b/files.tar.gz" | grep -q 'damson.kek' || fail "sqlite: the files archive lacks the key file"
	echo x >> "$b/database.sqlite"
	if DAMSON_DATABASE__DSN="file:$tmp/dst2.db" "$here/damson-restore.sh" "$b" >/dev/null 2>&1; then fail "sqlite: a tampered backup was restored"; fi
	rm -rf "$tmp/out"
	echo "backup test: sqlite ok"
else
	echo "backup test: sqlite3 not installed, skipping sqlite" >&2
fi

# --- PostgreSQL
if [ -n "${PGTEST_ADMIN_URL:-}" ]; then
	adm() { psql --dbname="$PGTEST_ADMIN_URL" --quiet -v ON_ERROR_STOP=1 -t -A "$@"; }
	adm -c "DROP DATABASE IF EXISTS bk_src" -c "DROP DATABASE IF EXISTS bk_dst" -c "CREATE DATABASE bk_src" -c "CREATE DATABASE bk_dst"
	base="${PGTEST_ADMIN_URL%/*}"
	psql --dbname="$base/bk_src" --quiet -v ON_ERROR_STOP=1 -c "CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('hello-pg');"
	export DAMSON_DATABASE__DRIVER=postgres DAMSON_DATABASE__DSN="$base/bk_src"
	"$here/damson-backup.sh" -o "$tmp/pgout" >/dev/null
	b="$(ls -d "$tmp"/pgout/damson-backup-*)"
	[ -f "$b/database.dump" ] || fail "postgres: no dump in $b"
	DAMSON_DATABASE__DSN="$base/bk_dst" "$here/damson-restore.sh" "$b" >/dev/null
	got="$(psql --dbname="$base/bk_dst" --quiet -t -A -c 'SELECT v FROM t')"
	[ "$got" = hello-pg ] || fail "postgres: restored data is '$got'"
	# A failing restore changes nothing (single transaction): the table exists already.
	if DAMSON_DATABASE__DSN="$base/bk_dst" "$here/damson-restore.sh" "$b" >/dev/null 2>&1; then fail "postgres: restoring into a non-empty database succeeded"; fi
	got="$(psql --dbname="$base/bk_dst" --quiet -t -A -c 'SELECT count(*) FROM t')"
	[ "$got" = 1 ] || fail "postgres: the failed restore changed the data ($got rows)"
	adm -c "DROP DATABASE bk_src" -c "DROP DATABASE bk_dst"
	echo "backup test: postgres ok"
fi
