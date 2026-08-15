# musik API v1

Base URL: `http://127.0.0.1:8787`  
OpenAPI: [`GET /api/openapi.json`](/api/openapi.json) · source [`openapi.yaml`](openapi.yaml)

Content-Type: `application/json` (кроме stream/artwork).  
Ошибки: `{"error":"…","code":"…"}`.

## Auth (один владелец)

| | |
|--|--|
| UI | пароль `MUSIK_PASSWORD` → `POST /api/auth/login` → httpOnly cookie `musik_session` |
| API / скрипты | `Authorization: Bearer <MUSIK_API_TOKEN>` |
| Отключить | только явно: `MUSIK_AUTH_DISABLED=1` |

Публично без auth: `GET /api/health` (без `tracks`/`dim`), `GET /api/openapi.json`, `GET /api/auth/me`, `POST /api/auth/login`, **`GET /listen/{token}`** (share radio).  
`POST /api/reload` — loopback **или** Bearer (callback worker).  
**Старт без пароля/токена запрещён**, кроме `MUSIK_AUTH_DISABLED=1`. Login rate-limit: 5/мин/IP.

Мобильная разработка: [MOBILE.md](MOBILE.md).

| Method | Path | Описание |
|--------|------|----------|
| POST | `/api/auth/login` | `{ "password": "…" }` → cookie, TTL ~14 дней |
| POST | `/api/auth/logout` | сброс cookie |
| GET | `/api/auth/me` | `{ok, auth_enabled}` |

## Core

| Method | Path | Описание |
|--------|------|----------|
| GET | `/api/health` | `{ok, version, api_version, auth}` (+ `tracks`, `dim` если auth) |
| GET | `/api/status` | tracks, maturity, mode, session, explore |
| GET | `/api/profile` | maturity, signals, top_artists/clusters, confidence |
| GET | `/api/library` | плоский список треков |
| GET | `/api/artists` | `{artists, count}` |
| GET | `/api/albums` | `{albums, count}` |
| GET | `/api/tracks/{id}` | один трек |
| GET | `/api/stream/{id}` | аудио (Range) |
| GET | `/api/artwork/{id}` | обложка |
| GET | `/api/similar/{id}` | top-10 cosine |
| POST | `/api/reload` | перечитать индекс из SQLite |
| GET | `/api/now` | current + queue (`?session_id=`) |
| GET | `/api/queue` | очередь |
| POST | `/api/queue/refresh` | пересобрать очередь |
| POST | `/api/events` | playback events |
| POST | `/api/session/start` | `{seed_track_id?}` |
| POST | `/api/session/jump` | прыжок по индексу в fixed playlist |
| POST | `/api/radio/start` | радио по вкусу (diverse seed) |
| POST | `/api/share/radio` | создать публичную ссылку на эфир |
| GET | `/api/share/radio` | список ссылок (`?all=1` — с отозванными) |
| DELETE | `/api/share/radio/{token}` | отозвать ссылку |
| GET | `/listen/{token}` · `/listen/{token}.mp3` | непрерывный MP3-эфир (публично, ffmpeg) |
| POST | `/api/play` | listen: track / artist / album / track_ids |

## Mixes & playlists

| Method | Path | Описание |
|--------|------|----------|
| GET | `/api/mixes` | полки |
| POST | `/api/mixes/{kind}/play` | играть микс |
| GET | `/api/playlists/daily/today` | daily |
| POST | `/api/playlists/daily/play` | играть daily |
| GET | `/api/playlists/{kind}/latest` | latest playlist |
| GET | `/api/later` | отложенные |
| POST | `/api/later` | `{track_id}` |
| DELETE | `/api/later` | `{track_id}` |

## Favorites & recommend

| Method | Path | Описание |
|--------|------|----------|
| GET | `/api/favorites` | tracks / artists / albums |
| POST | `/api/favorites` | добавить |
| DELETE | `/api/favorites` | убрать |
| POST | `/api/favorites/toggle` | toggle |
| GET | `/api/favorites/status` | `?type=track\|artist\|album&…` → `{favorited}` |
| GET | `/api/recommend/seed` | `?type=&track_id=\|artist=\|album=` → похожие треки |
| GET | `/api/recommend/favorites` | по центроидам избранного |
| GET | `/api/similar/artists` | похожие артисты |
| GET | `/api/similar/albums` | похожие альбомы |

## Jobs & discover

| Method | Path | Описание |
|--------|------|----------|
| GET | `/api/jobs` | `?status=` список |
| GET | `/api/jobs/{id}` | статус |
| POST | `/api/jobs/{kind}` | scan\|embed\|clusters\|daily\|album_tips\|full_rescan\|mix_pack |
| POST | `/api/library/rescan` | enqueue full_rescan |
| GET | `/api/discover/albums` | new album tips |
| GET | `/api/discover/resurfaced` | старый каталог |
| GET | `/api/metrics/weekly` | skip-rate, listens, diversity |

## Events `POST /api/events`

```json
{
  "type": "track_start|progress|track_end|skip|like|dislike",
  "track_id": 83,
  "session_id": "...",
  "position_sec": 12.5,
  "duration_sec": 240,
  "listened_sec": 12.5,
  "reason": "completed|skipped|next"
}
```

Ответ на skip/track_end: `{ok, next, queue, maturity, signed_weight, session_id, …}`.

## Сессии и вкус

- Каждая вкладка — свой `session_id` из `/api/radio/start`, `/api/play`, `/api/session/start`.
- Сессии **персистятся в SQLite** (`play_sessions`) и переживают рестарт player.
- Вкус (`user_profile_snapshots.context = global`) — Go EMA; дополнительно daypart (`morning|afternoon|evening|night`) мягко влияет на очередь (blend 0.7/0.3).
- `transitions` участвуют в score очереди аддитивно (`+ λ · norm(log1p(weight))`); без данных поведение как раньше.
- При N ≥ 8000 очередь строится по candidate pool (кластер + transitions + sample), иначе полный cosine.
- `offline_report` — Python CLI, не затирает global.

## Maturity

| maturity | условие | поведение |
|----------|---------|-----------|
| `discovering` | n_positive &lt; forming_at (default 3) | random/far, высокий explore |
| `forming` | &lt; ready_at (default 8) | смесь |
| `ready` | ≥ ready_at | вкус + continuity; старт радио — sample из top-K |

Пороги настраиваются через `MUSIK_PROFILE_FORMING_AT` и `MUSIK_PROFILE_READY_AT`.

## Share radio

`POST /api/share/radio` → `{ url, token }` — URL вида `http://host:8787/listen/<token>.mp3`.  
Вставь в VLC / браузер / любой HTTP-аудио клиент. Поток бесконечный (перекодирование через ffmpeg в MP3).  
Слушатели **не** обновляют вкус владельца. Лимит параллельных слушателей: `MUSIK_SHARE_MAX_LISTENERS` (default 4).

Для абсолютных URL с LAN задай `MUSIK_PUBLIC_BASE_URL`.

Env: `MUSIK_PASSWORD`, `MUSIK_API_TOKEN`, `MUSIK_SESSION_SECRET`, `MUSIK_AUTH_DISABLED`, `MUSIK_SECURE_COOKIE`, `MUSIK_PUBLIC_BASE_URL`, `MUSIK_FFMPEG`, `MUSIK_SHARE_BITRATE`, `MUSIK_SHARE_MAX_LISTENERS`, `MUSIK_PROFILE_*`, `MUSIK_WORKER_URL`.
