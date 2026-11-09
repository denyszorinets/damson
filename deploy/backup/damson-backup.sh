#!/bin/sh
# Backs up a Damson database and, optionally, files that are not in it (the
# configuration and key files). One run writes one folder:
#
#   OUT/damson-backup-<UTC time>/
#       database.dump | database.sqlite   PostgreSQL custom format | SQLite file
#       files.tar.gz                      only with -f (see below)
#       MANIFEST.sha256                   checksums of the files above
#
# The database comes from the controller's own settings:
#   DAMSON_DATABASE__DRIVER   postgres (default) or sqlite
#   DAMSON_DATABASE__DSN      the connection string / file path
# Needs pg_dump (postgres) or sqlite3 (sqlite) on PATH.
#
#   damson-backup.sh [-o OUT] [-f PATH]...
#     -o OUT   folder to write into (default ./backups)
#     -f PATH  also archive this file or folder (repeatable), for example
#              /etc/damson. The archive holds credentials in clear text: store
#              the backup folder like a secret, or encrypt it.
#
# What it does NOT back up: a KEK that lives only in the environment
# (DAMSON_KEK), the S3 bucket of the logs, the agents. See docs: Backup and restore.
set -eu
umask 077

out=./backups
files=""
while getopts o:f: opt; do
	case "$opt" in
		o) out="$OPTARG" ;;
		f) files="$files
$OPTARG" ;;
		*) echo "usage: damson-backup.sh [-o OUT] [-f PATH]..." >&2; exit 2 ;;
	esac
done

driver="${DAMSON_DATABASE__DRIVER:-postgres}"
dsn="${DAMSON_DATABASE__DSN:-}"
[ -n "$dsn" ] || { echo "damson-backup: DAMSON_DATABASE__DSN is not set" >&2; exit 2; }

dir="$out/damson-backup-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$dir"
fail() { rm -rf "$dir"; echo "damson-backup: $*" >&2; exit 1; }

case "$driver" in
	postgres)
		command -v pg_dump >/dev/null 2>&1 || fail "pg_dump is not installed"
		# The connection string is a command line argument, so ps shows it: keep the
		# password out of it and give it to libpq with PGPASSWORD or ~/.pgpass.
		pg_dump --format=custom --file="$dir/database.dump" --dbname="$dsn" || fail "pg_dump failed"
		db=database.dump
		;;
	sqlite)
		command -v sqlite3 >/dev/null 2>&1 || fail "sqlite3 is not installed"
		path="${dsn#file:}"
		path="${path%%\?*}"
		[ -f "$path" ] || fail "the SQLite file $path does not exist"
		# .backup copies a consistent snapshot, also while a controller runs.
		sqlite3 "$path" ".backup '$dir/database.sqlite'" || fail "sqlite3 .backup failed"
		db=database.sqlite
		;;
	*) fail "DAMSON_DATABASE__DRIVER must be postgres or sqlite, got $driver" ;;
esac

list="$db"
if [ -n "$files" ]; then
	# One archive; absolute paths are stored relative to / so a restore can put
	# them back with tar -C /.
	set --
	oldifs="$IFS"; IFS='
'
	for f in $files; do
		[ -e "$f" ] || { IFS="$oldifs"; fail "$f does not exist"; }
		set -- "$@" "${f#/}"
	done
	IFS="$oldifs"
	tar -C / -czf "$dir/files.tar.gz" "$@" || fail "archiving the files failed"
	list="$list files.tar.gz"
fi

( cd "$dir" && sha256sum $list > MANIFEST.sha256 )
echo "damson-backup: wrote $dir"
