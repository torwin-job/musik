# Deploy musik

Один владелец: пароль для UI, Bearer-токен для API. Worker не публикуется наружу.

## Auth (fail-closed)

Без `MUSIK_PASSWORD` и/или `MUSIK_API_TOKEN` player **не стартует**, пока явно не задано `MUSIK_AUTH_DISABLED=1` (только localhost-отладка).

| Переменная | Назначение |
|------------|------------|
| `MUSIK_PASSWORD` | вход в Web UI (cookie) |
| `MUSIK_API_TOKEN` | `Authorization: Bearer …` для API / Flutter |
| `MUSIK_SESSION_SECRET` | HMAC cookie |
| `MUSIK_SECURE_COOKIE=1` | cookie только по HTTPS |
| `MUSIK_AUTH_DISABLED=1` | открытый режим (не для публичного IP) |
| `MUSIK_CORS_ORIGINS` | список Origin через запятую для cross-origin (Flutter web); пусто = same-origin |

Login: не больше 5 попыток / IP / минуту (`429`).

## Docker Compose (рекомендуется)

```bash
cp .env.example .env
# обязательно: MUSIK_PASSWORD, MUSIK_API_TOKEN, MUSIK_SESSION_SECRET, MUSIK_LIBRARY

make up
make logs
make rescan && make mixes
make smoke
```

Volumes: `musik-data` → SQLite + кэши; библиотека RO из `MUSIK_LIBRARY`.

### Публичный VPS (белый IP)

1. Задай сильные секреты в `.env` (не `AUTH_DISABLED`).
2. Наружу только `:8787` (player). Worker не публикуй.
3. Reverse-proxy (Caddy/Nginx) → HTTPS.
4. Env:
   ```bash
   MUSIK_PUBLIC_BASE_URL=https://music.example.com
   MUSIK_SECURE_COOKIE=1
   ```
5. Бэкап: копируй `musik.db` (WAL) регулярно.
6. Play-сессии пишутся в SQLite — переживают рестарт контейнера.

### LAN / телефон

1. Compose уже публикует `8787`.
2. `http://<lan-ip>:8787` → пароль.
3. Для share-ссылок: `MUSIK_PUBLIC_BASE_URL=http://<lan-ip>:8787`.

### Share radio

Нужен **ffmpeg** (есть в `Dockerfile.player`).

```bash
MUSIK_PUBLIC_BASE_URL=https://music.example.com
# MUSIK_SHARE_BITRATE=192k
# MUSIK_SHARE_MAX_LISTENERS=4
```

UI **Поделиться** → `…/listen/<token>.mp3`. Отозвать в Профиле. Слушатели не меняют вкус.

## Makefile

| Target | Действие |
|--------|----------|
| `make up` / `down` / `logs` | Compose |
| `make rescan` / `mixes` | jobs (Bearer) |
| `make smoke` | HTTP smoke с auth |
| `make player` | сборка Go |

## Bare-metal

```bash
export MUSIK_PASSWORD=… MUSIK_API_TOKEN=…
export MUSIK_DB_PATH=$PWD/data/db/musik.db MUSIK_LIBRARY=/path/to/music
musik scan && musik embed && musik clusters
musik worker   # terminal 1
./player/bin/musik-player   # terminal 2
```

Schema: Python `init_db` / worker создаёт таблицы; Go при старте — safety-net `CREATE IF NOT EXISTS` (favorites, shares, play_sessions, …).

Подробнее: [API.md](API.md) · [MOBILE.md](MOBILE.md) · **[CAPACITY.md](CAPACITY.md)** (ресурсы под 50k треков).
