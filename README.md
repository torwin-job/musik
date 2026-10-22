# musik — локальный умный плеер

**musik** — self-hosted плеер для своей библиотеки MP3, FLAC и других
аудиофайлов. Папка на диске становится стримингом: веб-плеер, поиск,
избранное, персональное радио и полка миксов. Музыку проект не скачивает
и не продаёт.

Слушать можно одному или нескольким людям. Каталог файлов общий, а вкус,
история и плейлисты живут в профиле. Вход одного пароля остаётся режимом
по умолчанию; аккаунты включаются отдельно.

Сейчас это один Go-сервер и один Python-воркер. История рекомендательного
ядра — в [docs/ROADMAP.md](docs/ROADMAP.md).

## Что умеет

- сканирует библиотеку, читает теги и считает CLAP-векторы;
- играет в браузере и отдаёт файлы по HTTP, с текстами и обложками;
- хранит избранное, «потом», обычные и умные плейлисты;
- строит радио по вкусу и радио от трека, артиста, альбома или «забытого»;
- показывает полку миксов из `player/internal/playback/mixes.json`;
- отдаёт паспорт трека и экран недели в профиле;
- сам подхватывает новые файлы и умеет отдать непрерывный MP3-эфир по ссылке.

## Как это работает

1. Вы указываете путь к папке со своей музыкой.
2. Python-воркер сканирует файлы, читает метаданные и вычисляет аудиопризнаки.
3. CLAP преобразует звучание каждого трека в числовой вектор. Благодаря этому
   система сравнивает музыку по звуку, даже если жанры и теги заполнены плохо.
4. Alembic создаёт и обновляет общую схему SQLite или PostgreSQL. Go-сервер и
   Python worker проверяют один migration head перед стартом; приложения не
   выполняют миграции при запуске.
5. Лайки, пропуски и дослушивания обновляют вкус профиля. Обычное радио
   собирает очередь из этого вкуса. Радио от трека, артиста или альбома
   держит якорь в той же очереди, пока режим не сменён.

После первичного анализа GPU больше не обязателен: готовую базу и кеш
эмбеддингов можно перенести на обычный маломощный сервер.

**Стек:** Python (сканирование, CLAP, фоновые задачи) + Go (API, стриминг,
рекомендации, Web UI) + SQLite по умолчанию / PostgreSQL по `MUSIK_DATABASE_URL`.

**Доступ:** в режиме одного пароля — cookie после `MUSIK_PASSWORD` или
Bearer `MUSIK_API_TOKEN`. В режиме аккаунтов общий пароль и общий токен
выключены: вход по имени, приглашению или OIDC, у телефона свой токен
устройства.

---

## Архитектура

```
┌─────────────────────┐         ┌──────────────────────────┐
│  Web UI / телефон   │  HTTPS  │  Go player :8787         │
│  (browser / API)    │◄───────►│  один Server: auth, стрим│
└─────────────────────┘         │  на профиль: вкус, радио │
                                └────────────┬─────────────┘
                                             │ jobs HTTP
                                ┌────────────▼─────────────┐
                                │  Python worker :8790     │
                                │  scan · CLAP · mix_pack  │
                                └────────────┬─────────────┘
                                             │
                                ┌────────────▼─────────────┐
                                │  SQLite или PostgreSQL   │
                                │  data/db + data/cache    │
                                └──────────────────────────┘
```

| Слой | Где | Порт | Роль |
|------|-----|------|------|
| **Player** | `player/` (Go) | **8787** (публичный) | один HTTP-сервер: маршруты, auth, стрим, Web UI, радио и полка, share-radio (ffmpeg) |
| **Worker** | `src/musik/` (Python) | **8790** (только localhost / внутренняя сеть) | scan, CLAP embed, clusters, mix_pack, jobs. Миграции только здесь |
| **DB** | `data/db/musik.db` или `MUSIK_DATABASE_URL` | — | общий каталог; вкус, история и плейлисты — по профилю |
| **Кеши** | `data/cache/` | — | embeddings `.npy`, artwork |

Контракты и гайды:

| Документ | Содержание |
|----------|------------|
| [docs/API.md](docs/API.md) | HTTP API |
| [docs/openapi.yaml](docs/openapi.yaml) | тот же OpenAPI, что `player/internal/api/openapi.json` и `GET /api/openapi.json` |
| [docs/DEPLOY.md](docs/DEPLOY.md) | установка: SQLite / PostgreSQL / HTTPS / share |
| [docs/UPGRADE.md](docs/UPGRADE.md) | пошаговое обновление существующей установки |
| [docs/CONFIG.md](docs/CONFIG.md) | все переменные `.env` |
| [docs/MOBILE.md](docs/MOBILE.md) | телефон / Flutter |
| [docs/CAPACITY.md](docs/CAPACITY.md) | ресурсы под ~50k треков |
| [docs/ROADMAP.md](docs/ROADMAP.md) | история рекомендательного ядра, не продуктовый план |
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
| `data/db/musik.db` | SQLite база по умолчанию (+ `-wal` / `-shm` при работе); PostgreSQL задаётся через `MUSIK_DATABASE_URL` |
| `data/cache/embeddings/` | CLAP-векторы: `{md5}.{model}.{strategy}.npy` |
| `data/cache/artwork/` | обложки по hash |
| `data/music/` | опциональная локальная библиотека по умолчанию |
| `data/worker.log` и др. | служебные логи/эксперименты (можно игнорировать) |

Музыкальная коллекция обычно **не** внутри репо: путь задаётся `MUSIK_LIBRARY` (и монтируется RO в Docker).

**Бэкап / перенос / обновление:** сохраняй согласованный backup БД и отдельно аудио, кеши и конфигурацию. Порядок обновления и отката — в [инструкции обновления](docs/UPGRADE.md). Неизменённые аудиофайлы сохраняют MD5 и позволяют использовать прежний кеш эмбеддингов.

### `src/musik/` — Python

| Путь | Роль |
|------|------|
| `cli.py` | CLI: `musik scan`, `embed`, `clusters`, `artwork`, `worker`, … |
| `config.py` | настройки (`MUSIK_*`, пути к DB/cache) |
| `db/schema.py` | SQLite-совместимое описание исходной схемы |
| `db/alembic/` | общий Alembic head для SQLite и PostgreSQL |
| `db/transfer.py` | проверенный перенос SQLite → PostgreSQL без изменения источника |
| `db/migrations.py` | совместимость со старыми SQLite миграциями и запуск Alembic |
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

Точка входа — `cmd/musik-player`. Три пакета, на которые опирается остальной плеер:

- **`internal/api`** — HTTP: маршруты, JSON, cookie. Одна таблица маршрутов со scope `public`, `user`, `profile`, `admin`. Рядом, не внутри api: `app` (reload и воркер), `media` (обложки, транскод), `library` (загрузка и группировка), `playback`, `recommend`, `index` (общие эмбеддинги и счётчики профиля), `queue`, `taste`, `playlist`, `rules`, `db`, `auth` (пароль, Bearer, cookie, OIDC), `settings`, `themes`.
- **`internal/playback`** — сессии, радио и фиксированные очереди, события, полка. Радио от трека, артиста или альбома держит якорь в смеси вкуса, пока режим не сменён; «ближе» и «шире» — два шага на той же сессии. Единственный реестр полки — `mixes.json`.
- **`internal/db`** — SQLite и PostgreSQL. Один `Store` — это профиль на общем пуле. Миграций в Go нет: плеер только сверяет head схемы.

Web UI — `internal/static`. HTTP-тесты — `internal/apitest`. Сборка: `make player` → `player/bin/musik-player`.

### `scripts/`

| Скрипт | Назначение |
|--------|------------|
| `embed_rocm.sh` | `musik embed` через `.venv-rocm` + ROCm libs (AMD GPU) |
| `smoke_api.sh` | HTTP smoke с auth (`make smoke`) |
| `bench_queue.sh` | exact scan / queue и API benchmarks (`make bench`) |
| `sim_listener.py` | детерминированные listener-сценарии (`make sim`) |
| `export_paper_cosine.py` | утилита для cosine-таблиц / экспериментов |
| `musik-env.ps1` | Windows: чтение `.env`, health-проверки player/worker |
| `setup-windows.ps1` | Windows: установка одной командой (клон, зависимости, `.env`, сборка, запуск, автозапуск) |
| `setup-linux.sh` | Linux: то же самое + systemd user-сервисы |
| `musik.sh` | Linux: `start\|stop\|restart\|status\|logs` (через systemd, если сервисы установлены) |
| `start-musik.ps1` | Windows: запуск worker+player с `.env`, PID/логи, `-Restart`, `-InstallStartup` (автозапуск при входе) |
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

### Одной командой на своём ПК (Windows / Linux)

Скрипт сам скачает репозиторий, поставит недостающее (Git, Python 3.11+, Go,
ffmpeg — с вопросом перед установкой), создаст `.venv`, спросит папку с музыкой,
доступ с телефона и пароль, соберёт и запустит сервер, предложит автозапуск
и ежечасный перескан. Повторный запуск обновляет код и перезапускает сервер,
существующий `.env` не трогает.

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/torwin-job/musik/main/scripts/setup-windows.ps1 -OutFile $env:TEMP\musik-setup.ps1; powershell -ExecutionPolicy Bypass -File $env:TEMP\musik-setup.ps1
```

Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/torwin-job/musik/main/scripts/setup-linux.sh | bash
```

Без вопросов: `... -File $env:TEMP\musik-setup.ps1 -Dir D:\musik -Music D:\Music -Yes`
или `curl ... | bash -s -- --dir ~/musik --music ~/Music --yes`.
На Linux автозапуск — systemd user-сервисы (`musik-worker`, `musik-player`,
таймер `musik-scan`); без них — `scripts/musik.sh start|stop|restart|status|logs`.

Адрес сервера (только этот ПК / домашняя сеть / интернет), порт, папку с
музыкой и данные для подключения телефона (адреса, API-токен) потом можно
менять в веб-интерфейсе: **Профиль → Настройки**. Значения пишутся в `.env`,
кнопка «Перезапустить сервер» применяет их.

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

## Редакция веб-интерфейса

По умолчанию открывается исходный интерфейс. В «Профиле» можно выбрать
Mozgolika или [RaineDev](docs/RAINEDEV.md). Выбор сохраняется в браузере.
`MUSIK_UI=mozgolika` или `MUSIK_UI=rainedev` задаёт раскладку по умолчанию
для установки. Все три раскладки поддерживают аккаунты,
профили, приглашения и QR для подключения устройств.

## Сохранить базу при обновлении

Существующую установку не нужно создавать заново: база v5 обновляется до
текущей схемы со всей историей, избранным и плейлистами. По шагам — базовое
обновление на SQLite, несколько пользователей, OIDC, переезд на PostgreSQL —
в [UPGRADE.md](docs/UPGRADE.md). Go player сам схему не мигрирует; это делает
сервис `migrate` или команда `musik db migrate`.

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
(Главная · Сейчас · Библиотека · Плейлисты · Профиль). Страница «Сейчас» умещается в один
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
тот же трек загружается на паузе с той же секунды, «играть» продолжает. Нажал «играть» на
другом устройстве — это встанет на паузу. Записывает только устройство, где
сейчас играет; поздняя запись с перехваченного устройства не применяется.
Прослушивание не дублируется: время с обоих устройств складывается.
Открытая вкладка сама перезагружается после обновления сервера, как только
музыка на паузе (`/api/health` → `static`).

**Страница артиста.** Клик по артисту в библиотеке и на главной открывает его
треки, а не запускает их: «Слушать всё», «Радио» и «В плейлист» — отдельные
кнопки, клик по треку играет список артиста с этого места.

**Без иконок.** У вкладки нет значка приложения, манифест их не объявляет,
кнопки подписаны словами.

---

## Auth

Без входа player не стартует. В режиме одного пароля нужны `MUSIK_PASSWORD` или `MUSIK_API_TOKEN`; `MUSIK_AUTH_DISABLED=1` выключает проверку и не годится для публичного адреса. В режиме аккаунтов эти три переменные наоборот должны быть пустыми.

| | |
|--|--|
| UI, один пароль | `MUSIK_PASSWORD` → cookie `musik_session` |
| API, один пароль | `Authorization: Bearer $MUSIK_API_TOKEN` |
| Аккаунты | имя и пароль, приглашение или OIDC; у телефона свой токен устройства |
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

## Как слушает плеер

Очередь радио собирает одно ядро `BuildCore`. Обычное радио идёт по вкусу
профиля. Радио от трека, артиста, альбома или станции «забытое» смешивает
вектор якоря с этим вкусом и не заводит второй движок. В плеере это подпись
«радио от: …» и источник следующего трека («похоже на тебя», «часто после
этой», «чуть в сторону»). «Ближе» усиливает якорь, «шире» возвращает обычное
радио на той же сессии.

Полка миксов читается только из `mixes.json`. Паспорт трека — `GET /api/tracks/{id}`:
прослушивания, дослушивания, ранние скипы, последний раз, лайк или дизлайк,
год, длительность, громкость и жанры. Неделя профиля берёт время и топы из
`GET /api/metrics/weekly`; служебные метрики подбора туда не выводятся.

Тексты — `musik lyrics` и `GET /api/tracks/{id}/lyrics`. Новые файлы —
`musik watch`. Чужой плеер слушает `GET /listen/{token}.mp3` и вкус не меняет.
Зрелость профиля: `discovering` → `forming` → `ready`.

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
