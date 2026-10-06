#!/usr/bin/env bash
# Set up musik on this Linux machine with one command: clone the repository,
# install what is missing, configure .env, build, start, optional autostart
# (systemd user services).
#
#   curl -fsSL https://raw.githubusercontent.com/torwin-job/musik/main/scripts/setup-linux.sh | bash
#   curl -fsSL .../setup-linux.sh | bash -s -- --dir ~/musik --music /srv/music --yes
#
# Run from an existing clone (scripts/setup-linux.sh) it uses that clone.
# Repeated runs are safe: git pull, an existing .env is kept, services restart.
#
# Options: --dir DIR  --music DIR  --port N  --repo URL  --branch NAME  --yes
set -euo pipefail

REPO=https://github.com/torwin-job/musik.git
BRANCH=main
DIR=""
MUSIC=""
PORT=8787
YES=0

while [ $# -gt 0 ]; do
    case "$1" in
        --dir) DIR=$2; shift 2 ;;
        --music) MUSIC=$2; shift 2 ;;
        --port) PORT=$2; shift 2 ;;
        --repo) REPO=$2; shift 2 ;;
        --branch) BRANCH=$2; shift 2 ;;
        -y | --yes) YES=1; shift ;;
        -h | --help) sed -n '2,14p' "$0"; exit 0 ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

if [ -t 1 ]; then C=$'\033[36m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'; else C= G= Y= R= N=; fi
say() { printf '\n%s==> %s%s\n' "$C" "$*" "$N"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '    %s! %s%s\n' "$Y" "$*" "$N"; }
fail() { printf '    %sx %s%s\n' "$R" "$*" "$N" >&2; exit 1; }

# Piped into bash, stdin is the script itself: questions read the terminal.
TTY=/dev/tty
[ -r "$TTY" ] || YES=1

ask() { # ask "question" default -> REPLY
    if [ "$YES" = 1 ]; then REPLY=$2; return; fi
    printf '    %s [%s]: ' "$1" "$2" >"$TTY"
    IFS= read -r REPLY <"$TTY" || REPLY=""
    REPLY=${REPLY:-$2}
}

confirm() { # confirm "question" [Y|N]
    local def=${2:-Y} hint a
    if [ "$YES" = 1 ]; then [ "$def" = Y ]; return; fi
    [ "$def" = Y ] && hint="Y/n" || hint="y/N"
    while true; do
        printf '    %s [%s]: ' "$1" "$hint" >"$TTY"
        IFS= read -r a <"$TTY" || a=""
        case "${a,,}" in
            '') [ "$def" = Y ]; return ;;
            y | yes | д | да) return 0 ;;
            n | no | н | нет) return 1 ;;
        esac
    done
}

have() { command -v "$1" >/dev/null 2>&1; }

SUDO=""
[ "$(id -u)" -eq 0 ] || SUDO=sudo

PM=""
for p in apt-get dnf pacman zypper; do have "$p" && { PM=$p; break; }; done

pkg_install() { # pkg_install "what" pkg... — required: refusing stops setup
    local what=$1; shift
    [ -n "$PM" ] || fail "Не знаю пакетный менеджер этой системы. Установи $what вручную и запусти скрипт снова."
    confirm "Нужно установить: $what. Установить через $PM (попросит пароль sudo)?" ||
        fail "$what нужен для работы. Установи и запусти скрипт снова."
    pkg_run "$@"
}

pkg_run() {
    case "$PM" in
        apt-get) $SUDO apt-get update -qq && $SUDO apt-get install -y "$@" ;;
        dnf) $SUDO dnf install -y "$@" ;;
        pacman) $SUDO pacman -S --needed --noconfirm "$@" ;;
        zypper) $SUDO zypper --non-interactive install "$@" ;;
    esac
}

python_ok() { # python_ok cmd -> 3.11+
    "$1" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)' 2>/dev/null
}

go_ok() { # Go 1.21+ downloads the exact toolchain go.mod asks for by itself.
    have go || return 1
    local v
    v=$(go env GOVERSION 2>/dev/null || go version | awk '{print $3}')
    v=${v#go}
    [ "${v%%.*}" -gt 1 ] 2>/dev/null && return 0
    v=${v#*.}
    [ "${v%%.*}" -ge 21 ] 2>/dev/null
}

lan_ip() {
    local ip=""
    if have ip; then
        ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") {print $(i + 1); exit}}')
    fi
    [ -n "$ip" ] || ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    printf '%s' "$ip"
}

port_free() {
    if have ss; then ! ss -ltn "sport = :$1" 2>/dev/null | grep -q LISTEN; else return 0; fi
}

secret() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }
# head closes the pipe early; under pipefail that SIGPIPE must not abort setup.
password() { (LC_ALL=C tr -dc 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789' </dev/urandom 2>/dev/null | head -c 14) || true; }

# Same rules as the player's settings page: rewrite KEY= lines in place
# (or a commented "# KEY=" template), keep everything else, append the rest.
set_env() { # set_env file KEY value
    local file=$1 key=$2 val=$3 line
    case "$val" in *[[:space:]\#\'\"]*) val="'${val//\'/}'" ;; esac
    line="$key=$val"
    if grep -qE "^[[:space:]]*$key=" "$file"; then
        KEY=$key LINE=$line awk 'BEGIN{k=ENVIRON["KEY"]; l=ENVIRON["LINE"]} !done && $0 ~ "^[[:space:]]*" k "=" {print l; done=1; next} {print}' "$file" >"$file.tmp"
    elif grep -qE "^[[:space:]]*#[[:space:]]*$key=" "$file"; then
        KEY=$key LINE=$line awk 'BEGIN{k=ENVIRON["KEY"]; l=ENVIRON["LINE"]} !done && $0 ~ "^[[:space:]]*#[[:space:]]*" k "=" {print l; done=1; next} {print}' "$file" >"$file.tmp"
    else
        cp "$file" "$file.tmp" && printf '%s\n' "$line" >>"$file.tmp"
    fi
    mv "$file.tmp" "$file"
}

env_value() { # env_value file KEY
    sed -n "s/^[[:space:]]*$2=//p" "$1" | sed -n 1p | sed "s/^'\(.*\)'\$/\1/; s/^\"\(.*\)\"\$/\1/"
}

printf '\n%smusik — установка на Linux%s\n' "$G" "$N"
info 'Ответы в [скобках] — значения по умолчанию, Enter их принимает.'

# 1. Tools ---------------------------------------------------------------
say 'Проверяю git, Python, Go и ffmpeg'
if ! have git || ! have curl; then
    pkg_install 'git и curl' git curl
fi
info "git     $(git --version | awk '{print $3}')"

PY=""
for c in python3.13 python3.12 python3.11 python3; do
    if have "$c" && python_ok "$c"; then PY=$(command -v "$c"); break; fi
done
USE_UV=0
if [ -z "$PY" ]; then
    warn 'Python 3.11+ не найден (в системе слишком старый или его нет).'
    if confirm 'Скачать Python 3.12 через uv (в домашнюю папку, без sudo)?'; then
        have uv || curl -LsSf https://astral.sh/uv/install.sh | sh
        export PATH="$HOME/.local/bin:$HOME/.cargo/bin:$PATH"
        have uv || fail 'uv не установился.'
        USE_UV=1
    else
        fail 'Нужен Python 3.11+. Установи его и запусти скрипт снова.'
    fi
else
    info "python  $("$PY" --version | awk '{print $2}')"
    # Debian/Ubuntu split venv/pip into separate packages.
    if ! "$PY" -c 'import venv, ensurepip' 2>/dev/null; then
        case "$PM" in
            apt-get) pkg_install 'python3-venv' python3-venv python3-pip ;;
            *) warn 'модуль venv недоступен — установи пакет python3-venv своего дистрибутива' ;;
        esac
    fi
fi

GO_LOCAL="$HOME/.local/share/musik-go"
[ -x "$GO_LOCAL/bin/go" ] && export PATH="$GO_LOCAL/bin:$PATH"
if ! go_ok; then
    warn 'Go 1.21+ не найден (в репозиториях дистрибутива часто слишком старый).'
    confirm "Скачать Go с go.dev в $GO_LOCAL (без sudo)?" || fail 'Нужен Go 1.21+: https://go.dev/dl/'
    case "$(uname -m)" in
        x86_64 | amd64) arch=amd64 ;;
        aarch64 | arm64) arch=arm64 ;;
        armv6l | armv7l) arch=armv6l ;;
        *) fail "Не знаю архитектуру $(uname -m) — установи Go вручную: https://go.dev/dl/" ;;
    esac
    gov=$(curl -fsSL 'https://go.dev/VERSION?m=text' | sed -n 1p)
    rm -rf "$GO_LOCAL" && mkdir -p "$GO_LOCAL"
    curl -fsSL "https://go.dev/dl/$gov.linux-$arch.tar.gz" | tar -xz -C "$GO_LOCAL" --strip-components=1
    export PATH="$GO_LOCAL/bin:$PATH"
    go_ok || fail 'Go не установился.'
fi
info "go      $(go version | awk '{print $3}')"

if ! have ffmpeg && [ -n "$PM" ] &&
    confirm "ffmpeg не найден (нужен для «Поделиться радио» и мобильного потока). Установить через $PM?"; then
    case "$PM" in
        dnf) pkg_run ffmpeg-free || true ;;
        *) pkg_run ffmpeg || true ;;
    esac
fi
have ffmpeg && info 'ffmpeg  есть' || warn 'ffmpeg не найден — поделиться радио и мобильный поток работать не будут.'

# 2. Code ----------------------------------------------------------------
say 'Код musik'
HERE=""
if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "$(dirname -- "${BASH_SOURCE[0]}")/../player/go.mod" ]; then
    HERE=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
fi
if [ -z "$DIR" ]; then
    ask 'Куда установить musik' "${HERE:-$HOME/musik}"
    DIR=$REPLY
fi
DIR=${DIR/#\~/$HOME}
if [ -d "$DIR/.git" ]; then
    info "уже установлен в $DIR"
    if confirm 'Обновить код до последней версии (git pull)?'; then
        git -C "$DIR" pull --ff-only || warn 'git pull не удался (есть локальные изменения?) — продолжаю с текущим кодом.'
    fi
elif [ -d "$DIR" ] && [ -n "$(ls -A "$DIR" 2>/dev/null)" ]; then
    fail "Папка $DIR не пустая и это не musik. Укажи другую: --dir <папка>"
else
    git clone --branch "$BRANCH" "$REPO" "$DIR" || fail "Не удалось скачать $REPO"
fi
cd "$DIR"

# 3. Python environment ---------------------------------------------------
say 'Python-окружение (первый раз качает torch для анализа музыки)'
TORCH_INDEX="--extra-index-url https://download.pytorch.org/whl/cpu"
if have nvidia-smi && confirm 'Найдена видеокарта NVIDIA. Ставить torch с CUDA (быстрее анализ, ~3 ГБ)?' N; then
    TORCH_INDEX=""
else
    info 'torch без CUDA (~200 МБ вместо ~3 ГБ)'
fi
if [ ! -x .venv/bin/python ]; then
    if [ "$USE_UV" = 1 ]; then uv venv --python 3.12 .venv; else "$PY" -m venv .venv; fi
fi
if [ "$USE_UV" = 1 ]; then
    # shellcheck disable=SC2086
    VIRTUAL_ENV="$DIR/.venv" uv pip install $TORCH_INDEX -e . || fail 'установка зависимостей не удалась'
else
    .venv/bin/python -m pip install --upgrade pip --quiet
    # shellcheck disable=SC2086
    .venv/bin/python -m pip install $TORCH_INDEX -e . || fail 'pip install не удался — смотри ошибку выше.'
fi

# 4. Settings (.env) ------------------------------------------------------
say 'Настройки'
GENERATED_PW=""
if [ -f .env ]; then
    info '.env уже есть — оставляю его. Адрес и папку с музыкой можно поменять в веб-интерфейсе: Профиль → Настройки.'
    addr=$(env_value .env MUSIK_PLAYER_ADDR)
    [ -n "$addr" ] && PORT=${addr##*:}
else
    if [ -z "$MUSIC" ]; then
        default_music="$HOME/Music"
        [ -d "$default_music" ] || default_music="$HOME/Музыка"
        [ -d "$default_music" ] || default_music="$HOME/Music"
        ask 'Папка с музыкой' "$default_music"
        MUSIC=$REPLY
    fi
    MUSIC=${MUSIC/#\~/$HOME}
    if [ ! -d "$MUSIC" ]; then
        confirm "Папки $MUSIC нет. Создать?" && mkdir -p "$MUSIC" || fail 'Нужна папка с музыкой.'
    fi
    MUSIC=$(cd "$MUSIC" && pwd)
    while ! port_free "$PORT"; do
        warn "порт $PORT занят"
        ask 'Другой порт' "$((PORT + 1))"
        PORT=$REPLY
    done
    LAN=0
    confirm 'Открыть доступ с телефона и других устройств в домашней сети (Wi-Fi)?' && LAN=1
    PW=""
    if [ "$YES" != 1 ]; then
        printf '    Пароль для входа (Enter — придумать автоматически): ' >"$TTY"
        IFS= read -rs PW <"$TTY" || PW=""
        printf '\n' >"$TTY"
    fi
    if [ -z "$PW" ]; then PW=$(password); GENERATED_PW=$PW; fi
    cp .env.example .env
    chmod 600 .env
    set_env .env MUSIK_PASSWORD "$PW"
    set_env .env MUSIK_API_TOKEN "$(secret)"
    set_env .env MUSIK_SESSION_SECRET "$(secret)"
    set_env .env MUSIK_LIBRARY "$MUSIC"
    if [ "$LAN" = 1 ]; then
        set_env .env MUSIK_PLAYER_ADDR "0.0.0.0:$PORT"
        ip=$(lan_ip)
        [ -n "$ip" ] && set_env .env MUSIK_PUBLIC_BASE_URL "http://$ip:$PORT"
    else
        set_env .env MUSIK_PLAYER_ADDR "127.0.0.1:$PORT"
    fi
    # The launcher (systemd or musik.sh) starts the worker, not the player.
    set_env .env MUSIK_WORKER_AUTOSTART 0
    info ".env создан: $DIR/.env"
fi

# 5. Database and build ----------------------------------------------------
say 'База данных'
.venv/bin/musik db migrate || fail 'musik db migrate не удался.'

say 'Сборка плеера (Go)'
(cd player && go build -o bin/musik-player ./cmd/musik-player) || fail 'go build не удался — смотри ошибку выше.'
chmod +x scripts/musik.sh

# 6. Firewall ---------------------------------------------------------------
addr=$(env_value .env MUSIK_PLAYER_ADDR)
case "$addr" in 127.0.0.1:* | localhost:*) ;; *)
    if have ufw && $SUDO ufw status 2>/dev/null | grep -q 'Status: active'; then
        say 'Брандмауэр'
        confirm "Открыть порт $PORT в ufw, чтобы открывался телефон?" && $SUDO ufw allow "$PORT/tcp" || true
    elif have firewall-cmd && $SUDO firewall-cmd --state >/dev/null 2>&1; then
        say 'Брандмауэр'
        if confirm "Открыть порт $PORT в firewalld, чтобы открывался телефон?"; then
            $SUDO firewall-cmd --permanent --add-port="$PORT/tcp" && $SUDO firewall-cmd --reload || true
        fi
    fi
    ;;
esac

# 7. Autostart and start ---------------------------------------------------------
say 'Автозапуск и запуск'
UNIT_DIR="$HOME/.config/systemd/user"
if have systemctl && systemctl --user show-environment >/dev/null 2>&1 &&
    confirm 'Запускать musik автоматически (systemd, в том числе после перезагрузки)?'; then
    mkdir -p "$UNIT_DIR"
    # Go may live in ~/.local/share/musik-go; services do not read ~/.bashrc.
    cat >"$UNIT_DIR/musik-worker.service" <<EOF
[Unit]
Description=musik worker (scan, analysis, mixes)

[Service]
WorkingDirectory=$DIR
EnvironmentFile=$DIR/.env
Environment="MUSIK_ROOT=$DIR" "MUSIK_SUPERVISOR=systemd" "PYTHONUTF8=1"
ExecStart="$DIR/.venv/bin/musik" worker
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
    cat >"$UNIT_DIR/musik-player.service" <<EOF
[Unit]
Description=musik player (web UI and API)
After=musik-worker.service
Wants=musik-worker.service

[Service]
WorkingDirectory=$DIR
EnvironmentFile=$DIR/.env
Environment="MUSIK_ROOT=$DIR" "MUSIK_SUPERVISOR=systemd"
ExecStart="$DIR/player/bin/musik-player"
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF
    if confirm 'Добавить ежечасную проверку папки с музыкой и подтягивание обложек?'; then
        cat >"$UNIT_DIR/musik-scan.service" <<EOF
[Unit]
Description=musik: rescan library, fetch covers and artist photos

[Service]
Type=oneshot
WorkingDirectory=$DIR
EnvironmentFile=$DIR/.env
Environment="PYTHONUTF8=1"
# \${MUSIK_API_TOKEN} is expanded by systemd from EnvironmentFile.
ExecStart=/bin/sh -c 'curl -fsS -X POST -H "Authorization: Bearer \${MUSIK_API_TOKEN}" http://127.0.0.1:$PORT/api/library/rescan; "$DIR/.venv/bin/musik" artwork; "$DIR/.venv/bin/musik" artist-photos; curl -fsS -X POST -H "Authorization: Bearer \${MUSIK_API_TOKEN}" http://127.0.0.1:$PORT/api/reload'
EOF
        cat >"$UNIT_DIR/musik-scan.timer" <<EOF
[Unit]
Description=musik: hourly library rescan

[Timer]
OnBootSec=10min
OnUnitActiveSec=1h

[Install]
WantedBy=timers.target
EOF
        SCAN_TIMER=1
    fi
    systemctl --user daemon-reload
    systemctl --user enable musik-worker.service musik-player.service
    [ "${SCAN_TIMER:-0}" = 1 ] && systemctl --user enable --now musik-scan.timer
    # A running instance from scripts/musik.sh would hold the ports.
    sh scripts/musik.sh stop >/dev/null 2>&1 || true
    systemctl --user restart musik-worker.service musik-player.service
    if have loginctl && [ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null)" != yes ]; then
        if confirm 'Работать и без входа в систему (после перезагрузки сервера)? Нужен sudo для loginctl enable-linger.'; then
            $SUDO loginctl enable-linger "$USER" || warn 'не получилось — musik будет стартовать после входа в систему'
        fi
    fi
    info 'управление: systemctl --user status|restart|stop musik-player musik-worker'
else
    sh scripts/musik.sh restart || fail 'Сервер не запустился — логи: data/worker.log и data/player.log'
    info "управление: $DIR/scripts/musik.sh start|stop|restart|status|logs"
fi

LOCAL_URL="http://127.0.0.1:$PORT"
printf '    жду сервер'
for _ in $(seq 1 60); do
    curl -fsS -m 2 -o /dev/null "$LOCAL_URL/api/health" 2>/dev/null && break
    printf '.'
    sleep 1
done
printf '\n'

# 8. First scan -------------------------------------------------------------------
if confirm 'Просканировать музыку сейчас? (первый раз долго: каждый трек анализируется)'; then
    token=$(env_value .env MUSIK_API_TOKEN)
    curl -fsS -m 20 -X POST -H "Authorization: Bearer $token" "$LOCAL_URL/api/library/rescan" >/dev/null &&
        info 'сканирование запущено, прогресс — в веб-интерфейсе' || warn 'не удалось запустить сканирование'
fi

public=$(env_value .env MUSIK_PUBLIC_BASE_URL)
printf '\n%sГотово!%s\n' "$G" "$N"
info "На этом компьютере:  $LOCAL_URL"
[ -n "$public" ] && info "С телефона (Wi-Fi):  $public"
[ -n "$GENERATED_PW" ] && info "Пароль для входа:    $GENERATED_PW   (запиши его; он же в $DIR/.env)"
info 'Токен для приложения на телефоне и адреса — в веб-интерфейсе: Профиль → Настройки.'
