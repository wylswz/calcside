#!/bin/sh
# Hands the unprivileged worker a cgroup v2 subtree so
# --instance-memory-max can give each instance process its own group
# (memory cap + the console's resource usage), then drops root.
#
# Docker already delegates the container's cgroup (private cgroupns),
# but to root and mounted read-only: `--security-opt
# writable-cgroups=true` (Engine >= 28) makes it writable; chowning a
# subtree to the worker user and moving the init process out of the
# namespace root are left to us. Without a writable cgroupfs the worker
# starts anyway and runs instances uncapped.
set -eu

if [ "$(id -u)" != 0 ]; then
	exec "$@"
fi

cg=/sys/fs/cgroup
setup_cgroup() {
	# probe: cgroup v2 must be present and writable, with the memory
	# controller available for delegation
	[ -f "$cg/cgroup.controllers" ] || return 1
	grep -qw memory "$cg/cgroup.controllers" || return 1
	mkdir -p "$cg/init" 2>/dev/null || return 1
	mkdir -p "$cg/calcside/supervisor" 2>/dev/null || return 1
	# cgroup v2 only hands controllers down from a group without member
	# processes: park everything in init/, then give the worker
	# calcside/ (instance groups) with itself in calcside/supervisor.
	# Some listed PIDs exit between the read and the write — best-effort.
	for p in $(cat "$cg/cgroup.procs" 2>/dev/null); do
		echo "$p" >"$cg/init/cgroup.procs" 2>/dev/null || true
	done
	echo +memory >"$cg/cgroup.subtree_control" 2>/dev/null || return 1
	echo +memory >"$cg/calcside/cgroup.subtree_control" 2>/dev/null || return 1
	chown -R calcside:calcside "$cg/calcside" 2>/dev/null || return 1
	echo $$ >"$cg/calcside/supervisor/cgroup.procs" 2>/dev/null || return 1
	return 0
}

if setup_cgroup; then
	export CALCSIDE_INSTANCE_CGROUP_PARENT="${CALCSIDE_INSTANCE_CGROUP_PARENT:-/calcside}"
else
	echo "worker-entrypoint: per-instance memory limits disabled (cgroup v2/memory controller unavailable or not writable)" >&2
fi

exec su-exec calcside "$@"
