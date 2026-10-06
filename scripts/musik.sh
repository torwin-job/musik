#!/bin/sh
# musik on Linux/macOS: Python worker (8790) + Go player (8787).
#
#   scripts/musik.sh start | stop | restart | status | logs
#
# With the systemd user units from setup-linux.sh installed, the commands go
# to `systemctl --user`; otherwise the processes run in the background with
# pid files in data/run/. Both ways read .env from the project root (values
# already set in the environment win, as in start-musik.ps1).
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
RUN="$ROOT/data/run"
UNITS="musik-worker.service musik-player.service"

has_units() {
    command -v systemctl >/dev/null 2>&1 &&
        systemctl --user cat musik-player.service >/dev/null 2>&1
}

load_env() {
    [ -f "$ROOT/.env" ] || return 0
    while IFS= read -r line || [ -n "$line" ]; do
        line=$(printf '%s' "$line" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
        case "$line" in '' | '#'*) continue ;; esac
        key=${line%%=*}
        val=${line#*=}
        case "$key" in *[!A-Za-z0-9_]* | '') continue ;; esac
        case "$val" in
            \"*\") val=${val#\"}; val=${val%\"} ;;
            \'*\') val=${val#\'}; val=${val%\'} ;;
        esac
        if eval "[ -z \"\${$key+x}\" ]"; then
            export "$key=$val"
        fi
    done <"$ROOT/.env"
    export MUSIK_ROOT="${MUSIK_ROOT:-$ROOT}"
}

health_url() {
    addr=${MUSIK_PLAYER_ADDR:-:8787}
    case "$addr" in
        :*) addr="127.0.0.1$addr" ;;
        0.0.0.0:*) addr="127.0.0.1:${addr#0.0.0.0:}" ;;
    esac
    echo "http://$addr/api/health"
}

worker_url() {
    echo "${MUSIK_WORKER_URL:-http://127.0.0.1:8790}/jobs"
}

probe() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsS -m 3 -o /dev/null "$1" 2>/dev/null
    else
        "$ROOT/.venv/bin/python" -c "import urllib.request,sys; urllib.request.urlopen(sys.argv[1], timeout=3)" "$1" 2>/dev/null
    fi
}

wait_healthy() {
    i=0
    while [ $i -lt "${2:-45}" ]; do
        probe "$1" && return 0
        sleep 1
        i=$((i + 1))
    done
    return 1
}

running() {
    [ -f "$1" ] && kill -0 "$(cat "$1")" 2>/dev/null
}

stop_one() {
    if running "$RUN/$1.pid"; then
        pid=$(cat "$RUN/$1.pid")
        kill "$pid" 2>/dev/null || true
        i=0
        while kill -0 "$pid" 2>/dev/null && [ $i -lt 20 ]; do sleep 0.5; i=$((i + 1)); done
        kill -9 "$pid" 2>/dev/null || true
        echo "stopped $1 (pid $pid)"
    else
        echo "$1 is not running"
    fi
    rm -f "$RUN/$1.pid"
}

start_all() {
    load_env
    mkdir -p "$RUN"
    cd "$ROOT"
    if probe "$(worker_url)"; then
        echo "worker already healthy"
    else
        nohup "$ROOT/.venv/bin/musik" worker >>"$ROOT/data/worker.log" 2>&1 &
        echo $! >"$RUN/worker.pid"
        echo "worker started pid=$! (log: data/worker.log)"
        wait_healthy "$(worker_url)" 60 || { echo "worker did not start — see data/worker.log" >&2; exit 1; }
    fi
    if probe "$(health_url)"; then
        echo "player already healthy"
    else
        nohup "$ROOT/player/bin/musik-player" >>"$ROOT/data/player.log" 2>&1 &
        echo $! >"$RUN/player.pid"
        echo "player started pid=$! (log: data/player.log)"
        wait_healthy "$(health_url)" 60 || { echo "player did not start — see data/player.log" >&2; exit 1; }
    fi
    echo "musik is up: $(health_url | sed 's#/api/health##')"
}

stop_all() {
    stop_one player
    stop_one worker
}

cmd=${1:-start}
if has_units; then
    case "$cmd" in
        start | stop | restart | status) exec systemctl --user "$cmd" $UNITS ;;
        logs) exec journalctl --user -f -u musik-worker -u musik-player ;;
    esac
fi
case "$cmd" in
    start) start_all ;;
    stop) stop_all ;;
    restart)
        stop_all
        start_all
        ;;
    status)
        load_env
        for name in worker player; do
            if running "$RUN/$name.pid"; then echo "$name: running (pid $(cat "$RUN/$name.pid"))"; else echo "$name: stopped"; fi
        done
        ;;
    logs) exec tail -f "$ROOT/data/worker.log" "$ROOT/data/player.log" ;;
    *)
        echo "usage: $0 start|stop|restart|status|logs" >&2
        exit 2
        ;;
esac
