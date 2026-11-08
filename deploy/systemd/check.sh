#!/bin/sh
# Verifies the units with systemd-analyze. The binary, the service user and the
# environment files exist only on a real host, so the complaints about exactly
# those are ignored, as are warnings about other units of the machine (they come
# from the runner's own files); every other warning or error fails the check.
set -eu
dir="$(cd "$(dirname "$0")" && pwd)"
for u in damson-controller damson-agent; do
	out="$(systemd-analyze verify "$dir/$u.service" 2>&1 || true)"
	rest="$(printf '%s\n' "$out" | grep -v -e 'Command /usr/local/bin/damson is not executable' -e 'Failed to resolve user' -e 'Failed to resolve group' -e 'EnvironmentFile' -e '/usr/lib/systemd/' -e '/lib/systemd/' -e '/etc/systemd/' || true)"
	[ -z "$rest" ] || { echo "systemd-check: $u.service: $rest" >&2; exit 1; }
	# The units are hardened: dropping a hardening line fails the check.
	for key in NoNewPrivileges=yes ProtectSystem=strict PrivateTmp=yes RestrictSUIDSGID=yes CapabilityBoundingSet= EnvironmentFile= Restart=on-failure; do
		grep -q "^$key" "$dir/$u.service" || { echo "systemd-check: $u.service lacks $key" >&2; exit 1; }
	done
done
# The example env files carry placeholders, never a real credential.
for f in "$dir"/*.env.example; do
	grep -q 'CHANGE-ME' "$f" || { echo "systemd-check: $f has no CHANGE-ME placeholder" >&2; exit 1; }
done
# sysusers.d syntax.
systemd-sysusers --dry-run "$dir/damson.sysusers" >/dev/null 2>&1 || true
echo "systemd-check: ok"
