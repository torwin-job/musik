# musik — локальный умный плеер

**musik** — self-hosted музыкальный сервер и умный плеер для собственной
коллекции MP3, FLAC и других аудиофайлов. Он превращает папку с музыкой на
компьютере или сервере в личный стриминговый сервис: с веб-плеером, поиском,
избранным, персональным радио и автоматически собранными миксами.

Проект нужен тем, кто хранит музыку у себя и хочет слушать её с разных
устройств, не загружая коллекцию в сторонние сервисы. Библиотека, история
прослушиваний и музыкальный профиль остаются на вашем диске.

> musik не скачивает и не продаёт музыку. Для работы нужна собственная
> легально полученная аудиотека. Проект рассчитан на одного владельца.

## Что умеет

- сканировать локальную музыкальную библиотеку и читать теги;
- воспроизводить музыку в браузере и отдавать её через HTTP API;
- искать треки, хранить избранное и историю прослушиваний;
- анализировать звучание треков с помощью CLAP и находить похожую музыку;
- строить персональное радио, которое постепенно учитывает вкус владельца;
- создавать Daily Mix и тематические подборки;
- автоматически обрабатывать новые файлы, добавленные в библиотеку;
- показывать тексты песен и обложки;
- создавать ссылку на непрерывный MP3-эфир для другого плеера;
- работать локально, на домашнем сервере или VPS.

## Как это работает

1. Вы указываете путь к папке со своей музыкой.
2. Python-воркер сканирует файлы, читает метаданные и вычисляет аудиопризнаки.
3. CLAP преобразует звучание каждого трека в числовой вектор. Благодаря этому
   система сравнивает музыку по звуку, даже если жанры и теги заполнены плохо.
4. Go-сервер хранит каталог и историю в SQLite, отдаёт Web UI и стримит
   аудиофайлы на телефон или компьютер.
5. Лайки, пропуски и прослушивания обновляют профиль вкуса. Радио смешивает
   похожие треки, историю, время суток и небольшую долю новых рекомендаций.

После первичного анализа GPU больше не обязателен: готовую базу и кеш
эмбеддингов можно перенести на обычный маломощный сервер.

**Стек:** Python (сканирование, CLAP, фоновые задачи) + Go (API, стриминг,
рекомендации, Web UI) + SQLite.

**Доступ:** пароль для Web UI и Bearer-токен для API.

---

## Архитектура

```
┌─────────────────────┐         ┌──────────────────────┐
│  Web UI / телефон   │  HTTPS  │  Go player :8787     │
│  (browser / API)    │◄───────►│  auth · stream · EMA │
└─────────────────────┘         │  Radio · share MP3   │
                                └──────────┬───────────┘
                                           │ jobs HTTP
                                ┌──────────▼───────────┐
                                │  Python worker :8790 │
                                │  scan · CLAP · mixes │
                                └──────────┬───────────┘
                                           │
                                ┌──────────▼───────────┐
                                │  SQLite + caches     │
                                │  data/db + data/cache│
                                └──────────────────────┘
```

| Слой | Где | Порт | Роль |
|------|-----|------|------|
| **Player** | `player/` (Go) | **8787** (публичный) | API, auth, стрим файлов, вкус, Radio/Daily, Web UI, share-radio (ffmpeg) |
| **Worker** | `src/musik/` (Python) | **8790** (только localhost / внутренняя сеть) | scan, CLAP embed, clusters, mix_pack, jobs |
| **DB** | `data/db/musik.db` | — | треки, фичи, сессии, favorites, jobs |
| **Кеши** | `data/cache/` | — | embeddings `.npy`, artwork |

Контракты и гайды:

| Документ | Содержание |
|----------|------------|
| [docs/API.md](docs/API.md) | HTTP API |
| [docs/openapi.yaml](docs/openapi.yaml) | OpenAPI (`/api/openapi.json`) |
| [docs/DEPLOY.md](docs/DEPLOY.md) | Docker / VPS / HTTPS / share |
| [docs/MOBILE.md](docs/MOBILE.md) | телефон / Flutter |
| [docs/CAPACITY.md](docs/CAPACITY.md) | ресурсы под ~50k треков |
| [docs/ROADMAP.md](docs/ROADMAP.md) | план развития |
| [docs/PUBLISHING.md](docs/PUBLISHING.md) | безопасная публикация backend и Flutter |
| [mobile/README.md](mobile/README.md) | заметки по мобильному клиенту |

---

## Карта репозитория

```
musik/
├── README.md                 ← этот файл
├── pyproject.toml            ← Python-пакет `musik`, CLI entrypoint
├── Makefile                  ← up/down/rescan/mixes/smoke/bench/player
├── docker-compose.yml        ← player + worker
├── Dockerfile.player         ← Go + ffmpeg
├── Dockerfile.worker         ← Python pipeline
├── .env.example              ← шаблон секретов и путей
├── .env                      ← локальные секреты (не в git)
│
├── docs/                     ← документация
├── scripts/                  ← утилиты
├── src/musik/                ← Python: scan / embed / jobs / worker
├── player/                   ← Go: API + UI
├── mobile/README.md          ← контракт отдельного Flutter-клиента
├── tests/                    ← pytest + go tests рядом
└── data/                     ← runtime (БД, кеши; в git почти пусто)
```

### Корень и инфраструктура

| Путь | Назначение |
|------|------------|
| `pyproject.toml` | зависимости Python, скрипт `musik` |
| `Makefile` | `make up`, `rescan`, `mixes`, `smoke`, `bench`, `player` |
| `docker-compose.yml` | сервисы `player` (:8787) и `worker` (без публикации порта) |
| `Dockerfile.player` / `Dockerfile.worker` | образы |
| `.env` / `.env.example` | `MUSIK_*` переменные |
| `.github/workflows/ci.yml` | CI |
| `.venv/` | обычный Python venv (часто CUDA-wheels — на AMD GPU не подходит) |
| `.venv-rocm/` | отдельный venv Python 3.12 + ROCm torch (локально, в `.gitignore`) |
| `.rocm-extra/` | локально распакованные ROCm-libs при необходимости (`.gitignore`) |

### `data/` — что лежит на диске в рантайме

| Путь | Что это |
|------|---------|
| `data/db/musik.db` | основная SQLite (+ `-wal` / `-shm` при работе) |
| `data/cache/embeddings/` | CLAP-векторы: `{md5}.{model}.{strategy}.npy` |
| `data/cache/artwork/` | обложки по hash |
| `data/music/` | опциональная локальная библиотека по умолчанию |
| `data/worker.log` и др. | служебные логи/эксперименты (можно игнорировать) |

Музыкальная коллекция обычно **не** внутри репо: путь задаётся `MUSIK_LIBRARY` (и монтируется RO в Docker).

**Бэкап / перенос на сервер:** копируй `data/db/musik.db` (+ wal/shm при остановленных процессах) и `data/cache/embeddings/`. Файлы аудио на сервере должны иметь те же MD5 (те же байты) — эмбеддинги подтянутся из кеша.

### `src/musik/` — Python

| Путь | Роль |
|------|------|
| `cli.py` | CLI: `musik scan`, `embed`, `clusters`, `worker`, … |
| `config.py` | настройки (`MUSIK_*`, пути к DB/cache) |
| `db/schema.py` | схема SQLite |
| `db/store.py` | чтение/запись треков, embeddings, jobs |
| `scanner/` | обход библиотеки, теги, MD5, LUFS/BPM/key |
| `embed/clap.py` | модель CLAP (GPU/CPU) |
| `embed/segments.py` | окна start/middle/end × 30 с @ 48 kHz |
| `embed/pipeline.py` | очередь эмбеддинга + прогресс |
| `embed/cache.py` | дисковый `.npy` кеш |
| `index/clusters.py` | кластеры по cosine |
| `brain/` | генераторы миксов / explain |
| `discover/` | album tips и т.п. |
| `jobs/` | очередь задач + runner + progress в DB |
| `worker/server.py` | HTTP worker `:8790` |
| `listen/` | история / профиль (offline) |

### `player/` — Go

| Путь | Роль |
|------|------|
| `cmd/musik-player/main.go` | точка входа |
| `internal/config/` | env Go-плеера |
| `internal/auth/` | пароль, cookie, Bearer, rate-limit логина |
| `internal/db/` | SQLite RO/RW со стороны player |
| `internal/index/` | матрица эмбеддингов в RAM, `SimsTo` (параллельно при N≥1500) |
| `internal/taste/` | EMA-вкус |
| `internal/queue/` | скоринг очереди: taste + transition + daypart + candidate pool |
| `internal/api/` | HTTP handlers (radio, catalog, share, jobs proxy, …) |
| `internal/static/` | Web UI: `index.html`, `app.js`, `style.css` |

Сборка: `make player` → `player/bin/musik-player`.

### `scripts/`

| Скрипт | Назначение |
|--------|------------|
| `embed_rocm.sh` | `musik embed` через `.venv-rocm` + ROCm libs (AMD GPU) |
| `smoke_api.sh` | HTTP smoke с auth (`make smoke`) |
| `bench_queue.sh` | бенч `radio/start` (`make bench`) |
| `export_paper_cosine.py` | утилита для cosine-таблиц / экспериментов |

### `docs/` и `tests/`

Документация — см. таблицу выше. Тесты: `tests/*.py` (pytest), `player/internal/.../*_test.go`.

---

## Быстрый старт

### Docker (рекомендуется на сервере)

```bash
cp .env.example .env
# обязательно: MUSIK_PASSWORD, MUSIK_API_TOKEN, MUSIK_SESSION_SECRET, MUSIK_LIBRARY

make up          # http://127.0.0.1:8787
make rescan      # scan+embed+… через jobs
make mixes
make smoke
```

Подробности: [docs/DEPLOY.md](docs/DEPLOY.md).

### Локально без Docker (CPU / NVIDIA CUDA venv)

```bash
python -m venv .venv && source .venv/bin/activate
pip install -e ".[dev]"

export MUSIK_ROOT=$PWD
export MUSIK_DB_PATH=$PWD/data/db/musik.db
export MUSIK_LIBRARY=/path/to/music
export MUSIK_PASSWORD=…
export MUSIK_API_TOKEN=…

musik scan && musik embed && musik clusters

make player
./player/bin/musik-player   # при MUSIK_WORKER_AUTOSTART=1 сам поднимет worker
```

UI: http://127.0.0.1:8787

### AMD GPU (ROCm) — прогон embed на ПК

Обычный `.venv` с CUDA-wheels **не** увидит Radeon. Нужен ROCm torch (у нас: `.venv-rocm`, Python 3.12).

```bash
# после настройки .venv-rocm (см. ниже «AMD / ROCm»)
./scripts/embed_rocm.sh              # только pending
./scripts/embed_rocm.sh --force      # пересчитать всё
```

Проверено на современной AMD GPU с поддерживаемой версией ROCm. Старые
карты без официальной поддержки PyTorch/ROCm могут не работать.

Ориентиры на тестовой библиотеке (~116 треков, `--force`):

| Устройство | Время |
|------------|--------|
| Современная AMD GPU | десятки секунд |
| Современный desktop CPU | несколько минут |

На ~50k: GPU порядка часов; CPU — сильно дольше. Имеет смысл считать на
рабочей станции, затем скопировать `data/db` + `data/cache/embeddings` на
сервер без GPU.

---

## Пайплайн данных (с нуля)

```bash
# 1) чистый старт (остановить player/worker!)
rm -f data/db/musik.db data/db/musik.db-wal data/db/musik.db-shm
rm -rf data/cache/embeddings/* data/cache/artwork/*

# 2) схема создастся сама при первом запуске / init
export MUSIK_LIBRARY=/path/to/music

# 3) теги + audio features
musik scan                 # или: musik scan --tags-only (быстрее, без LUFS/BPM)

# 4) CLAP (лучше GPU)
musik embed                # или ./scripts/embed_rocm.sh
# прогресс: CLI + GET /api/jobs/{id} → .progress

# 5) кластеры / миксы
musik clusters
# или POST /api/jobs/mix_pack

# 6) плеер
./player/bin/musik-player
```

Во время **embed** грузятся и CPU, и GPU — это нормально:

- **CPU** — decode MP3/FLAC, resample, подготовка тензоров (`librosa`)
- **GPU** — inference CLAP
- На трек: 3 окна × 30 с (начало / середина / конец) → средний вектор 512-d

---

## Auth

Fail-closed: без пароля/токена player **не стартует**, пока не `MUSIK_AUTH_DISABLED=1` (не для публичного IP).

| | |
|--|--|
| UI | `MUSIK_PASSWORD` → cookie `musik_session` |
| API | `Authorization: Bearer $MUSIK_API_TOKEN` |
| Login | ≤ 5 попыток / IP / мин → `429` |

Ключевые env — в `.env.example`. Для большой библиотеки см. также `MUSIK_WORKERS`, `MUSIK_CANDIDATE_POOL_AT` ([docs/CAPACITY.md](docs/CAPACITY.md)).

### Генерация секретов

Никогда не записывайте реальные значения в исходники, Dockerfile или
документацию. Локальный `.env` игнорируется Git.

```bash
umask 077
cp .env.example .env
openssl rand -hex 24  # MUSIK_PASSWORD
openssl rand -hex 32  # MUSIK_API_TOKEN
openssl rand -hex 32  # MUSIK_SESSION_SECRET
```

После утечки или публикации APK со встроенным токеном значения необходимо
ротировать. Правила публикации уязвимостей описаны в [SECURITY.md](SECURITY.md).

---

## Возможности плеера (кратко)

- Каталог / поиск / стрим файлов
- **Radio** по EMA-вкусу: top-K sample старта, explore, recently-played penalty
- Transition boost + daypart; при большом N — **candidate pool** (`MUSIK_CANDIDATE_POOL_AT`, default 8000)
- Daily / mixes / favorites
- **Тексты:** `musik lyrics` (LRCLIB) → UI «Текст песни» / `GET /api/tracks/{id}/lyrics`
- **Watch:** `musik watch` — новые файлы в `MUSIK_LIBRARY` → scan/embed/mixes/reload сами
- **Share radio:** непрерывный MP3 через ffmpeg → `GET /listen/{token}.mp3`
- Maturity профиля: `discovering` → `forming` → `ready`
- Jobs с прогрессом (scan/embed/full_rescan)

---

## AMD / ROCm (кратко)

1. Системный ROCm (`rocm-smi`, `/opt/rocm`) + Python **3.12** (wheels до 3.13; системный 3.14 не подходит).
2. Venv `.venv-rocm` + torch/triton с [repo.radeon.com](https://repo.radeon.com/rocm/manylinux/) под вашу версию ROCm.
3. При нехватке `libhipsparselt`: `sudo pacman -S hipsparselt` (Arch) или локальный `.rocm-extra`.
4. Запуск: `./scripts/embed_rocm.sh` (выставляет `LD_LIBRARY_PATH`).

Проверка:

```bash
.venv-rocm/bin/python -c "import torch; print(torch.cuda.is_available(), torch.cuda.get_device_name(0))"
# True <supported AMD GPU>
```

CPU-only тест (спрятать GPU):

```bash
CUDA_VISIBLE_DEVICES= HIP_VISIBLE_DEVICES= .venv-rocm/bin/musik embed --force
```

---

## Make-цели

| Target | Действие |
|--------|----------|
| `make up` / `down` / `logs` | Docker Compose |
| `make player` | сборка Go → `player/bin/musik-player` |
| `make rescan` | `POST /api/library/rescan` (Bearer) |
| `make mixes` | `POST /api/jobs/mix_pack` |
| `make smoke` | smoke API |
| `make bench` | бенч radio/start |
| `make test-go` | `go test` |

---

## Перенос на сервер (после GPU-прогона на ПК)

1. На ПК: `scan` → `embed` (ROCm) → `clusters`.
2. Остановить player/worker.
3. Скопировать на сервер:
   - `data/db/musik.db` (и wal/shm, либо после checkpoint)
   - `data/cache/embeddings/`
4. На сервере: та же (байтово) музыкальная библиотека, `MUSIK_LIBRARY=…`, запуск Compose/player **без** обязательного GPU.

---

## Ёмкость ~50k

Сводка: **8–16 GB RAM**, GPU желателен для первого embed, player держит матрицу ~100 MB на 50k×512. Детали и env: [docs/CAPACITY.md](docs/CAPACITY.md).

---

## Лицензия

[MIT](LICENSE).
