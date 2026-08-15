# Roadmap — Умный локальный плеер (ТЗ 3.0)

| Шаг | Содержание | Статус |
|-----|------------|--------|
| 1–5 | Python scan/embed/index/brain/listen | done |
| 5b | Go realtime player | done |
| **0** | API contract + `/api/profile` | done → [API.md](API.md) |
| **1** | Cold start / discover maturity | done |
| **2** | Видимый профиль (artists/clusters) | done |
| **3** | Radio VK Mix (`/api/radio/start`) | done |
| **4** | Daily playlist API + jobs | done |
| **5** | Album discover + resurfaced tips | done |
| **6** | Python worker (`musik worker`) | done |
| **7** | Full Web UI (multi-view + PWA) | done |
| **8** | Mobile via PWA / LAN | done → [mobile/README.md](../mobile/README.md) |
| **9** | Metrics, artwork, cluster soft-boost, tests | done |
| **10** | Single-owner auth (password + Bearer) | done |
| **11** | API v1 catalog / recommend / OpenAPI | done |
| **12** | Docker Compose + DEPLOY | done → [DEPLOY.md](DEPLOY.md) |
| **13** | Radio start diversity (top-K sample) | done |
| **14** | Share radio (continuous MP3 via ffmpeg) | done |
| **15** | Mobile client guide | done → [MOBILE.md](MOBILE.md) |
| **16** | Auth fail-closed + session persist + smarter radio | done |
| **17** | Job progress (scan/embed) + candidate pool + parallel SimsTo | done |
| **18** | CAPACITY.md + ROCm embed path (`.venv-rocm` / `embed_rocm.sh`) | done |
| **19** | Full README map (paths, pipeline, transfer) | done |

## Дальше (не сделано / по желанию)

| # | Что | Зачем | Приоритет |
|---|-----|--------|-----------|
| A | Прогон **реальных ~50k** на 9070 → копирование DB+embeddings на VPS | твой продакшен | **сейчас** |
| B | Публичный деплой: Compose + HTTPS + `MUSIK_PUBLIC_BASE_URL` | слушать с телефона/работы | **сейчас** |
| C | Flutter app (`mobile/flutter/`, план в MOBILE.md B) | native клиент вместо PWA | средний |
| D | ANN / HNSW вместо brute+pool | если 50k+pool всё ещё медленно | низкий |
| E | Инкрементальный watch (`musik watch`) | авто scan/embed/mixes | **done** |
| F | Бэкап-скрипт / cron sqlite `.backup` | не потерять вкус/сессии | средний |

ТЗ 3.0 core (шаги 0–16) — **закрыт**. Дальше — эксплуатация и клиенты.

## Запуск

См. [DEPLOY.md](DEPLOY.md) и корневой `Makefile` (`up`, `down`, `logs`, `rescan`, `mixes`, `smoke`).

```bash
# bare-metal
export MUSIK_DB_PATH=$PWD/data/db/musik.db MUSIK_LIBRARY=/path/to/music
export MUSIK_PASSWORD=… MUSIK_API_TOKEN=…
musik worker   # terminal 1
./player/bin/musik-player   # terminal 2 → http://127.0.0.1:8787
```

После новых файлов: `make rescan` или UI → worker `full_rescan` → auto-reload.
