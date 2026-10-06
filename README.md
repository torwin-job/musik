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

Текущий runtime работает и пригоден для локального использования. Следующее
направление разработки — корректный lifecycle рекомендаций и единое
ранжирующее ядро. Актуальный статус, критерии и порядок поставки находятся в
**[docs/ROADMAP.md](docs/ROADMAP.md)**; старый план «ТЗ 3.0» больше не
используется.

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
4. Python-мигратор создаёт и обновляет SQLite. Go-сервер проверяет точную версию
   схемы, отдаёт Web UI и стримит аудиофайлы на телефон или компьютер.
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
| **DB** | `data/db/musik.db` | — | catalog, events, recommendation foundation, sessions, favorites, jobs |
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
├── tools/                    ← разовые инструменты библиотеки (CUE, дубли, артисты)
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
| `Makefile` | `make up`, `rescan`, `mixes`, `smoke`, `sim`, `bench`, `player` |
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

**Бэкап / перенос / обновление:** копируй `data/db/musik.db` (+ wal/shm при остановленных процессах) и `data/cache/embeddings/`. Файлы аудио должны иметь те же MD5 (те же байты) — эмбеддинги подтянутся из кеша. Как поднять старую базу на новой версии — ниже, в [«Сохранить базу при обновлении»](#сохранить-базу-при-обновлении).

### `src/musik/` — Python

| Путь | Роль |
|------|------|
| `cli.py` | CLI: `musik scan`, `embed`, `clusters`, `artwork`, `worker`, … |
| `config.py` | настройки (`MUSIK_*`, пути к DB/cache) |
| `db/schema.py` | базовая SQLite-схема совместимости |
| `db/migrations.py` | пронумерованные миграции и `PRAGMA user_version` |
| `db/store.py` | чтение/запись треков, embeddings, jobs |
| `scanner/` | обход библиотеки, теги, MD5, LUFS/BPM/key |
| `embed/clap.py` | модель CLAP (GPU/CPU) |
| `embed/segments.py` | интервалы start/middle/end × 30 с, подаются окнами по 10 с @ 48 kHz |
| `embed/pipeline.py` | очередь эмбеддинга + прогресс |
| `embed/cache.py` | дисковый `.npy` кеш |
| `index/clusters.py` | кластеры по cosine |
| `brain/` | генераторы миксов / explain |
| `discover/` | album tips и т.п. |
| `artwork/` | пакетная загрузка обложек (iTunes) в `data/cache/artwork` |
| `jobs/` | очередь задач + runner + progress в DB |
| `worker/server.py` | HTTP worker `:8790` |
| `listen/` | история / профиль (offline) |

### `player/` — Go

| Путь | Роль |
|------|------|
| `cmd/musik-player/main.go` | точка входа |
| `internal/config/` | env Go-плеера |
| `internal/auth/` | пароль, cookie, Bearer, rate-limit логина |
| `internal/db/` | SQLite access и точный schema-version gate; миграций в Go нет |
| `internal/index/` | матрица эмбеддингов в RAM, `TopK`, `SimsTo`, fused exact scan, сегменты соавторов из `artist_segments` |
| `internal/taste/` | EMA-вкус |
| `internal/queue/` | единое ядро очереди: candidates → features → score → select |
| `internal/library/` | правила библиотеки: безопасные пути загрузки, группировка artist/album |
| `internal/playback/` | сессии, радио/плейлист, события прослушивания, вкус |
| `internal/recommend/` | похожие треки / артисты / альбомы |
| `internal/api/` | HTTP: маршруты, JSON, CORS; файлы по эндпоинтам |
| `internal/apitest/` | HTTP-тесты плеера (через публичный Handler) |
| `internal/static/` | Web UI: `index.html`, `app.js`, `style.css`, иконки PWA |

Тесты: HTTP — `internal/apitest`; домен — рядом с пакетом (`internal/library`, `internal/recommend`, `internal/playback`); SQLite — `internal/db`.

Сборка: `make player` → `player/bin/musik-player`.

### `scripts/`

| Скрипт | Назначение |
|--------|------------|
| `embed_rocm.sh` | `musik embed` через `.venv-rocm` + ROCm libs (AMD GPU) |
| `smoke_api.sh` | HTTP smoke с auth (`make smoke`) |
| `bench_queue.sh` | exact scan / queue и API benchmarks (`make bench`) |
| `sim_listener.py` | детерминированные listener-сценарии (`make sim`) |
| `export_paper_cosine.py` | утилита для cosine-таблиц / экспериментов |
| `musik-env.ps1` | Windows: чтение `.env`, health-проверки player/worker |
| `start-musik.ps1` | Windows: запуск worker+player с `.env`, PID/логи, `-InstallStartup` (автозапуск при входе) |
| `auto-scan.ps1` | Windows: плановый перескан библиотеки (`-InstallTask`, по умолчанию каждые 60 мин) |

### `tools/`

| Инструмент | Назначение |
|------------|------------|
| `cue_split.py` | нарезка CUE-образов (`.cue` + FLAC/APE/WAV) в отдельные треки (`--apply`) |
| `cleanup_duplicates.py` | удаление файлов, помеченных как дубли (dry-run по умолчанию, `--apply`) |

### `docs/` и `tests/`

Документация — см. таблицу выше. Тесты: `tests/*.py` (pytest); Go — `player/internal/<пакет>/*_test.go` (`make test-go`).

---

## Быстрый старт

### Docker (рекомендуется на сервере)

```bash
cp .env.example .env
# обязательно: MUSIK_PASSWORD, MUSIK_API_TOKEN, MUSIK_SESSION_SECRET, MUSIK_LIBRARY

make up          # worker migrate + healthcheck → player → http://127.0.0.1:8787
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

musik db migrate
musik scan && musik embed && musik clusters

make player
musik worker                # terminal 1; повторно проверяет/применяет migrations
./player/bin/musik-player   # terminal 2; только проверяет точную версию схемы
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

## Сохранить базу при обновлении

Старую установку **не нужно** поднимать с нуля. Треки, эмбеддинги, история
прослушиваний, избранное и вкус живут в SQLite и кеше на диске. Новая версия
приложения только мигрирует схему.

Не удаляй `data/db/musik.db` и не запускай блок [«Пайплайн данных (с нуля)»](#пайплайн-данных-с-нуля), если хочешь сохранить библиотеку.

### Что копировать

Останови player и worker, затем сохрани:

| Путь | Зачем |
|------|--------|
| `data/db/musik.db` | каталог, история, вкус, избранное, миксы, сессии |
| `data/db/musik.db-wal` и `musik.db-shm` | хвост транзакций, если файлы есть |
| `data/cache/embeddings/` | готовые CLAP-векторы — без них 2k треков снова считаются часами |
| `data/cache/artwork/` | обложки (по желанию) |

Сами аудиофайлы в базу не входят: путь к ним задаёт `MUSIK_LIBRARY`.

Проверка, что WAL дописан (после остановки процессов):

```bash
sqlite3 data/db/musik.db 'PRAGMA wal_checkpoint(TRUNCATE);'
```

### Как перенести на новую версию

1. Положи старые файлы на те же места (или укажи `MUSIK_DB_PATH` на копию базы).
2. В `.env` выставь **ту же папку музыки**, что была раньше (`MUSIK_LIBRARY`).
3. Обнови схему — треки и история не стираются:

   ```bash
   musik db migrate
   ```

   Повторный вызов безопасен. Player стартует только если `PRAGMA user_version`
   совпадает с поддерживаемой версией (сейчас 7).
4. Запусти worker и player как обычно. Scan/embed подхватят уже посчитанное
   по MD5 файла. Пересчитаются только новые или изменённые треки.

Миксы «на сегодня» и дни недели можно пересобрать отдельно — вкус из истории
останется:

```bash
make mixes    # или POST /api/jobs/mix_pack
```

### Чего не делать

- Не указывай `MUSIK_LIBRARY` на меньшую папку и не гоняй `full_rescan` /
  `make rescan` против неё: пути, которых нет на диске, помечаются неактивными,
  и большая коллекция пропадёт из каталога, хотя строки в базе ещё будут.
- Не перекодируй файлы «для порядка» перед переносом: сменится MD5, и CLAP
  пойдёт заново. Те же байты — тот же кеш.
- Не копируй `musik.db`, пока player или worker ещё пишут: будет обрезанный WAL.

Если базы нет и нужна именно чистая установка — тогда следующий раздел.

---

## Пайплайн данных (с нуля)

```bash
# 1) чистый старт (остановить player/worker!)
rm -f data/db/musik.db data/db/musik.db-wal data/db/musik.db-shm
rm -rf data/cache/embeddings/* data/cache/artwork/*

# 2) создать актуальную схему до запуска player
export MUSIK_LIBRARY=/path/to/music
musik db migrate

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
- На трек: 3 интервала × 30 с (начало / середина / конец) → средний вектор 512-d
- Интервал подаётся в модель окнами по 10 с — это её вход (`max_length_s=10`).
  Одно длинное окно она обрезала бы до **случайных** 10 с (`truncation="rand_trunc"`):
  остальные 20 с декодировались бы впустую, а вектор одного и того же файла
  получался бы разным при каждом пересчёте. Девять окон по 10 с = те же три
  интервала, но прослушанные целиком и воспроизводимо

---

## Обслуживание библиотеки

Разовые операции для чистоты каталога. Destructive-шаги по умолчанию делают
dry-run — сначала посмотри вывод, потом повтори с `--apply`.

### Нарезка CUE-образов

Альбомные образы (`.cue` + FLAC/APE/WAV) режутся на отдельные треки, битые
входы ретраятся, нулевые файлы и `.cue` убираются:

```bash
python tools/cue_split.py "M:/music"           # dry-run
python tools/cue_split.py "M:/music" --apply
```

Если пути не переданы, берётся `MUSIK_LIBRARY`. Когда не задано ни то, ни
другое, инструмент останавливается — чужой диск по умолчанию он не читает.

### Дубликаты: FLAC важнее MP3

`musik scan` помечает копии по цепочке: одинаковый MD5 → chromaprint →
метаданные. Группа метаданных — `artist+title+album+year+is_remaster`, и копии
сливаются только если длительности совпадают в пределах 3 с: ремастеры,
лайв/студийные дубли и переиздания остаются отдельными треками. Имя альбома в
базе **не срезается** — суффикс вроде «… (2001 Remastered)» остаётся в теге, а
флаг `is_remaster` (миграция 6) лишь помечает трек: иначе оригинал и ремастер
попали бы в один альбом двумя копиями каждой песни. Внутри группы выигрывает
лучший файл: сначала формат (FLAC > WAV/AIFF > Opus/OGG > M4A/AAC > MP3), затем
битрейт и размер.

```bash
musik scan                              # пометить дубли
python tools/cleanup_duplicates.py      # dry-run: что будет удалено
python tools/cleanup_duplicates.py --apply   # удалить файлы с диска
```

### Составные артисты

Кредит вида «Thomas / БИ-2 / Сплин» разбирается **один раз при скане**, и
сегменты пишутся в базу (`tracks.artist_segments`, миграция 7); API и UI просто
показывают то, что сохранено. Разделители — только явные: `/`, `;` и
`feat.`/`ft.`. Знаки `&` и «и» разделителями не считаются: «Simon & Garfunkel»,
«Король и Шут» и «Earth, Wind & Fire» остаются целыми, а подстрока «Би-2»
внутри «Би-2 & Сплин» не цепляет чужой релиз. Варианты написания имён
(БИ-2 → Би-2) не переписываются — имя берётся из тега файла.

### Обложки для треков без embedded-art

```bash
musik artwork            # iTunes API → data/cache/artwork/{md5}.jpg
```

Отдельная команда, не шаг скана. Сначала она ищет картинку **в папке
альбома**: `cover`/`folder`/`front`/`обложка`/`f` (`.jpg`, `.png`, `.webp`), затем
подпапки `Scans`/`Covers`/`обложки`; папка диска («CD 2», «2. Bonus CD») берёт
обложку альбома. Чужие картинки вроде `Band.jpg` не подхватываются. Только
если в папке ничего нет, идёт запрос в iTunes: один на альбом, артист
совпадает целиком, альбом — целиком плюс суффикс издания вроде «(Remastered)».
Для кириллицы сначала спрашивается российский магазин (в американском
«Сплин» — это «Splean»), потом американский. Между запросами 3 с: iTunes
пропускает около 20 в минуту, на 429/403 команда ждёт `Retry-After` и
повторяет, а если ограничение не снимается — останавливается и не помечает
альбомы как ненайденные. Альбом без совпадения запоминается на 30 дней
(`artwork_lookups`, миграция 9), так что повторный запуск спрашивает только
про новое. Кеш — `data/cache/artwork/{md5}.jpg`. Обложка перекачивается только
если `artwork_path` пуст **или файла на диске нет**; `--force` перекачивает всё.

### Фото артистов

```bash
musik artist-photos      # Deezer API → data/cache/artwork/artists/
```

У iTunes фото артистов нет, у Deezer есть — с исходным написанием имён
(«Сплин», «Би-2»). Принимается только точное совпадение имени (без учёта
регистра и пробелов); из одноимённых выбирается страница с большим числом
подписчиков. Артист без совпадения запоминается на 30 дней (`artist_photos`,
миграция 9). Фото показываются на плитках артистов, на главной и в шапке
страницы артиста (`GET /api/artist-photos`); где фото нет, остаётся обложка.

На Windows ежечасный `auto-scan.ps1` после пересканирования запускает обе
команды и перезагружает индекс плеера.

---

## Интерфейс и PWA

Web UI — обычная страница: открывается в браузере или добавляется на домашний
экран телефона как приложение (PWA через `manifest.webmanifest`).

**Мобильная вёрстка.** На узких экранах (≤760px) включается «приложенческий»
каркас: шапка и нижнее меню закреплены, прокручивается только содержимое
вкладки. Верхнее меню уезжает вниз и превращается в иконки
(Главная · Сейчас · Библиотека · Плейлисты). Страница «Сейчас» умещается в один
экран без прокрутки, ползунок громкости скрыт (остаётся системная громкость).
Очередь радио открывается кнопкой **«Дальше в радио»** в отдельном окне.

**Оценки прямо в списке.** У каждого трека в очереди и плейлисте справа есть
♥ (в избранное + сигнал «нравится») и перечёркнутое ♥ («не нравится»); на
десктопе они видны в строке трека.

**Экран блокировки (Media Session).** Обложка, название и исполнитель уходят в
операционную систему — лок-скрин, шторка уведомлений, кнопки на гарнитуре
(play/pause, следующий/предыдущий, перемотка). Пользовательские кнопки
♥/⊘ в системном виджете iOS и Chrome не поддерживаются, поэтому они не
регистрируются — только стандартный транспорт и метаданные.

**Продолжение на другом устройстве.** Сервер хранит, где ты слушаешь
(`GET/PUT /api/playback/state`, миграция 8): сессию, трек, позицию и
прослушанное время. Открыл страницу на телефоне или перезагрузил вкладку —
тот же трек загружается на паузе с той же секунды, ▶ продолжает. Нажал ▶ на
другом устройстве — это встанет на паузу. Записывает только устройство, где
сейчас играет; поздняя запись с перехваченного устройства не применяется.
Прослушивание не дублируется: время с обоих устройств складывается.
Открытая вкладка сама перезагружается после обновления сервера, как только
музыка на паузе (`/api/health` → `static`).

**Страница артиста.** Клик по артисту в библиотеке и на главной открывает его
треки, а не запускает их: «▶ Слушать всё», «Радио» и «В плейлист» — отдельные
кнопки, клик по треку играет список артиста с этого места.

**Иконки.** Логотип и иконки приложения — в `player/internal/static/icons/`
(`icon-192`, `icon-512`, `icon-maskable-512`, `apple-touch-icon` 180×180,
`favicon-32`, `logo`). Манифест отдаёт их из Go; на iPhone иконка
подхватывается через `apple-touch-icon`, на Android — из манифеста.

---

## Auth

Fail-closed: без пароля/токена player **не стартует**, пока не `MUSIK_AUTH_DISABLED=1` (не для публичного IP).

| | |
|--|--|
| UI | `MUSIK_PASSWORD` → cookie `musik_session` |
| API | `Authorization: Bearer $MUSIK_API_TOKEN` |
| Login | ≤ 5 попыток / IP / мин → `429` |

Ключевые env — в `.env.example`. Для большой библиотеки см. также
`MUSIK_WORKERS` и exact fused scan
([docs/CAPACITY.md](docs/CAPACITY.md)).

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
- **Radio** по текущему EMA-вкусу: одно ядро `BuildCore`, learned ranker и gated Thompson
- Текущий runtime: fused exact scan, source quotas, finish/skip мониторинг в профиле.
  Дальше: [ROADMAP](docs/ROADMAP.md)
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
| `make sim` | детерминированный listener simulator |
| `make bench` | exact fused scan, queue build и API latency |
| `make test-go` | `go test` |

---

## Перенос на сервер (после GPU-прогона на ПК)

То же, что при [обновлении со старой базы](#сохранить-базу-при-обновлении): копируется SQLite и кеш эмбеддингов, не музыка заново.

1. На ПК: `scan` → `embed` (ROCm) → `clusters`.
2. Остановить player/worker.
3. Скопировать на сервер:
   - `data/db/musik.db` (и wal/shm, либо после checkpoint)
   - `data/cache/embeddings/`
4. На сервере: та же (байтово) музыкальная библиотека, `MUSIK_LIBRARY=…`, `musik db migrate`, запуск Compose/player **без** обязательного GPU.

---

## Ёмкость ~50k

Сводка: **8–16 GB RAM**, GPU желателен для первого embed, player держит матрицу ~100 MB на 50k×512. Детали и env: [docs/CAPACITY.md](docs/CAPACITY.md).

---

## Лицензия

[MIT](LICENSE).
