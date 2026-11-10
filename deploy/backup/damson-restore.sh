#!/bin/sh
# Restores the database from a folder written by damson-backup.sh into an
# EMPTY target database, after checking the checksums (Postgres: needs
# pg_restore; one transaction). It never touches the
# files archive: unpack that yourself (tar -C / -xzf files.tar.gz) once you
# have looked at it.
#
#   damson-restore.sh BACKUP_DIR
#
# The target is DAMSON_DATABASE__DRIVER / DAMSON_DATABASE__DSN. Stop every
# controller and agent first (docs: Backup and restore). A Postgres target must
# be an empty database; a SQLite target file must not exist.
set -eu
umask 077

[ $# -eq 1 ] && [ -d "$1" ] || { echo "usage: damson-restore.sh BACKUP_DIR" >&2; exit 2; }
dir="$1"
driver="${DAMSON_DATABASE__DRIVER:-postgres}"
dsn="${DAMSON_DATABASE__DSN:-}"
[ -n "$dsn" ] || { echo "damson-restore: DAMSON_DATABASE__DSN is not set" >&2; exit 2; }

( cd "$dir" && sha256sum --strict -c MANIFEST.sha256 ) || { echo "damson-restore: the backup does not match its checksums" >&2; exit 1; }

case "$driver" in
	postgres)
		command -v pg_restore >/dev/null 2>&1 || { echo "damson-restore: pg_restore is not installed" >&2; exit 1; }
		[ -f "$dir/database.dump" ] || { echo "damson-restore: $dir/database.dump is missing (a SQLite backup?)" >&2; exit 1; }
		# One transaction: the dump applies completely or not at all. The connection
		# string is a command line argument (ps shows it): keep the password out of
		# it and use PGPASSWORD or ~/.pgpass.
		pg_restore --dbname="$dsn" --single-transaction --exit-on-error --no-owner --no-privileges "$dir/database.dump"
		;;
	sqlite)
		[ -f "$dir/database.sqlite" ] || { echo "damson-restore: $dir/database.sqlite is missing (a Postgres backup?)" >&2; exit 1; }
		path="${dsn#file:}"
		path="${path%%\?*}"
		[ ! -e "$path" ] || { echo "damson-restore: $path exists; refusing to overwrite it" >&2; exit 1; }
		cp "$dir/database.sqlite" "$path"
		;;
	*) echo "damson-restore: DAMSON_DATABASE__DRIVER must be postgres or sqlite, got $driver" >&2; exit 2 ;;
esac
echo "damson-restore: database restored from $dir"
