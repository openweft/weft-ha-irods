#!/bin/sh
# Entrypoint for the irods-ha image : spawn the iRODS server in the
# background, then exec weft-ha-irods in the foreground. The trap
# ensures both processes die together — if iRODS crashes the agent
# goes down (so the L4 pool sees an offline replica and weft-agent
# reschedules) ; conversely if the agent dies we tear iRODS down so
# the next-boot agent re-bootstraps cleanly.
#
# We pass through every CLI flag to weft-ha-irods. iRODS itself reads
# /etc/irods/server_config.json (rendered by weft-init before the
# microVM boots ; that's why the agent doesn't render it here).

set -eu

# /etc/init.d/irods is the upstream sysv script installed by the
# irods-server debian package. It backgrounds irodsServer + iRODS'
# delay-server and writes a pid file under /var/run/irods/.
IRODS_INIT="${IRODS_INIT:-/etc/init.d/irods}"

# 1. Spawn iRODS in the background. The init script returns once the
#    server pid is up ; the actual irodsServer process is the
#    long-running child.
"${IRODS_INIT}" start

# Resolve the irodsServer pid the init script just started. The
# package writes it to /var/run/irods/irods.pid ; if that's missing
# we fall back to pgrep.
irods_pid=""
if [ -f /var/run/irods/irods.pid ]; then
    irods_pid="$(cat /var/run/irods/irods.pid 2>/dev/null || true)"
fi
if [ -z "${irods_pid}" ]; then
    irods_pid="$(pgrep -x irodsServer | head -1 || true)"
fi
if [ -z "${irods_pid}" ]; then
    echo "entrypoint: could not resolve irodsServer pid — refusing to start agent" >&2
    exit 1
fi

# 2. Trap signals + child exit. If iRODS dies first we propagate ;
#    if the agent dies first we kill iRODS before exiting.
cleanup() {
    rc=$?
    if kill -0 "${irods_pid}" 2>/dev/null; then
        # Use the init script for a clean shutdown when possible so
        # iRODS gets a chance to close its DB connection pool.
        "${IRODS_INIT}" stop 2>/dev/null || kill -TERM "${irods_pid}" 2>/dev/null || true
        for _ in 1 2 3 4 5 6 7 8 9 10; do
            if ! kill -0 "${irods_pid}" 2>/dev/null; then
                break
            fi
            sleep 1
        done
        kill -KILL "${irods_pid}" 2>/dev/null || true
    fi
    exit "${rc}"
}
trap cleanup EXIT INT TERM

# 3. Background watcher : if iRODS exits, kill our own PID so the
#    foreground agent gets SIGTERM and the trap above tears down.
(
    # Poll the pid because irodsServer is not our direct child (the
    # init script daemonised it) so `wait` would block forever.
    while kill -0 "${irods_pid}" 2>/dev/null; do
        sleep 2
    done
    echo "irodsServer (pid ${irods_pid}) exited — bringing the replica down" >&2
    kill -TERM $$ 2>/dev/null || true
) &

# 4. Exec the agent in the foreground.
exec /usr/local/bin/weft-ha-irods agent "$@"
