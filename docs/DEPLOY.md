# Установка musik

Новая установка через Docker Compose. Для существующей установки —
[UPGRADE.md](UPGRADE.md), все переменные `.env` — [CONFIG.md](CONFIG.md).

Состав: **player** (Go, веб-интерфейс и API, порт 8787), **worker** (Python:
скан, эмбеддинги CLAP, задания; наружу не публикуется), **migrate** (готовит базу
и завершается) и, по желанию, **postgres**. Player и worker стартуют только после
успешного migrate. Go player схему не меняет, только проверяет её версию.

## 1. Код и образы

```bash
git clone https://github.com/torwin-job/musik.git
cd musik
cp .env.example .env
```

Образы — готовые или свои, как в [шаге 1 обновления](UPGRADE.md#1-новая-версия):

- готовые: `./scripts/update-compose.sh` (файл `docker-compose.images.yml`);
- своя сборка: `./scripts/update-compose.sh --build` (файл `docker-compose.yml`).

Этот же скрипт запускает установку с нуля и применяет любые изменения `.env`.

## 2. SQLite (по умолчанию)

Подходит для одного слушателя и небольших компаний. Заполните `.env`
(подробно — [CONFIG.md](CONFIG.md)):

```dotenv
MUSIK_LIBRARY=/srv/music
MUSIK_PASSWORD=<openssl rand -hex 16>
MUSIK_API_TOKEN=<openssl rand -hex 32>
MUSIK_SESSION_SECRET=<openssl rand -hex 32>
```

Запуск:

```bash
./scripts/update-compose.sh            # или --build
```

База создаётся в томе `musik-data` (`/data/db/musik.db`). Откройте
`http://<адрес>:8787`, войдите паролем, в «Загрузке» нажмите
«Пересканировать». Первый скан скачает модель CLAP (около 600 МБ) и посчитает
эмбеддинги; прогресс виден там же.

## 3. PostgreSQL

Для больших библиотек и нескольких пользователей. Встроенный PostgreSQL 17
работает в томе `musik-postgres` и не публикует порт наружу. К `.env` из
раздела 2 добавьте:

```dotenv
COMPOSE_PROFILES=postgres
MUSIK_POSTGRES_PASSWORD=<openssl rand -hex 32>
MUSIK_DATABASE_URL=postgresql://musik:<тот же пароль>@postgres:5432/musik?sslmode=disable
```

и запустите тот же `./scripts/update-compose.sh`. `migrate` создаст схему в
пустой базе.

Внешний PostgreSQL: не задавайте `COMPOSE_PROFILES`, укажите в
`MUSIK_DATABASE_URL` его адрес и `sslmode=require`. Пользователь из URL должен
владеть пустой базой.

Резервная копия: `docker compose exec postgres pg_dump -U musik -Fc musik > musik.dump`.
SQLite копирует сервис `migrate` перед каждым изменением схемы (`/data/backups`).

## 4. Несколько пользователей и OIDC

Настраивается одинаково для SQLite и PostgreSQL, на новой установке сразу:
вместо `MUSIK_PASSWORD`/`MUSIK_API_TOKEN` задайте переменные из
[раздела 3 CONFIG.md](CONFIG.md#3-вход-несколько-пользователей), поставьте
HTTPS reverse proxy и откройте приглашение первого администратора. Порядок
действий — [шаги 3 и 4 UPGRADE.md](UPGRADE.md#3-несколько-пользователей); на
новой установке у администратора просто нет прежних данных.

## HTTPS

Нужен для входа нескольких пользователей и желателен всегда при доступе из
интернета. Опубликуйте player только на localhost и поставьте proxy:

```yaml
# docker-compose.override.yml
services:
  player:
    ports: !override
      - "127.0.0.1:8787:8787"
```

```caddyfile
music.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

В `.env`: `MUSIK_PUBLIC_BASE_URL=https://music.example.com`, `MUSIK_SECURE_COOKIE=1`.
Override-файл передавайте скрипту вместе с основным:
`./scripts/update-compose.sh -f docker-compose.images.yml -f docker-compose.override.yml`.

## Телефон в локальной сети

Compose публикует порт 8787. Откройте `http://<IP компьютера>:8787`. Для QR и
ссылок «поделиться» задайте `MUSIK_PUBLIC_BASE_URL=http://<IP>:8787`. QR
подключения приложения — в «Профиль → Настройки сервера» (один владелец) или
«Профиль → Устройства и токены» (несколько пользователей).

## Поделиться радио

«Поделиться» создаёт ссылку `…/listen/<token>.mp3`, которую открывает VLC,
браузер или телефон. Слушатели не меняют ваш вкус; отозвать ссылку можно в
«Профиле». Нужен `MUSIK_PUBLIC_BASE_URL`; битрейт и число слушателей —
`MUSIK_SHARE_BITRATE`, `MUSIK_SHARE_MAX_LISTENERS`.

## Темы

Плеер читает темы с диска при каждом запросе, перезапуск не нужен. В Docker это
`/data/themes` на томе `musik-data`:

```bash
docker compose cp themes/ink player:/data/themes/ink
```

```text
data/themes/ink/
  theme.json      # id (= имя папки), name, blurb, swatch, color
  theme.css       # стили для html[data-theme="<id>"]
  fonts/          # необязательно, .woff2 / .woff
```

Папка с `id` встроенной темы заменяет её файлы. Своя палитра из «Профиля»
хранится только в браузере.

## Плагины

Образы player и worker плагины не содержат и ничего не скачивают. Manifest и
executable ставит администратор. Протокол — [sdk/plugin/v1/README.md](../sdk/plugin/v1/README.md).

В `.env` укажите **один** каталог на хосте (несколько путей через `:` Compose
не монтирует):

```dotenv
MUSIK_PLUGIN_DIRS=/srv/musik/plugins
```

Compose монтирует его в player **на запись** как `/plugins` и внутри контейнера
задаёт `MUSIK_PLUGIN_DIRS=/plugins`. Запись нужна самому плагину: cookies и
прочие его файлы живут рядом с executable, плеер их не хранит. В каталоге лежит
`*.json` manifest; executable — файл из поля `executable`, относительно этого
каталога. Образ player не включает интерпретатор плагина: executable должен
запускаться в контейнере сам (статический бинарник или скрипт с интерпретатором,
который уже есть в образе).

Библиотека у player тоже смонтирована **на запись** (`/music`): плагин пишет
туда готовый файл через временный `.part` и атомарное переименование. Worker
монтирует ту же библиотеку **только для чтения** и после успешного импорта
выполняет обычный `full_rescan` (scan, embed, reload). У player `/music` и
`/plugins` остаются с правом записи.

Без Docker плеер сканирует `MUSIK_PLUGIN_DIRS` как есть. Если переменная пустая,
каталог — `$XDG_DATA_HOME/musik/plugins` или `~/.local/share/musik/plugins`.
Таймаут и параллелизм — `MUSIK_PLUGIN_TIMEOUT` (900 секунд) и
`MUSIK_PLUGIN_CONCURRENCY` (2).

### Яндекс Музыка

В репозитории есть protocol v1 плагин с поиском и импортом треков, альбомов и
артистов. Соберите executable из каталога `player/`:

```sh
go build -o plugins/yandex/yandex-plugin ./cmd/yandex-plugin
```

Установите `MUSIK_PLUGIN_DIRS` на `player/plugins/yandex` или скопируйте туда
`manifest.json` и executable. Положите OAuth token в файл `token` рядом с
executable и ограничьте доступ учётной записью player (например, mode `0600`).
В multi-user установке этот v1 плагин использует один серверный token и доступен
администратору; protocol v1 пока не предоставляет отдельный вход Яндекса для
каждого профиля. Дополнительные настройки описаны в
[`player/plugins/yandex/README.md`](../player/plugins/yandex/README.md).

## Makefile

| Цель | Действие |
| --- | --- |
| `make up` / `down` / `logs` | Compose со своей сборкой |
| `make rescan` / `mixes` | Задания через API, нужен `MUSIK_API_TOKEN` в окружении |
| `make smoke` | Проверка API |
| `make player` | Сборка Go player |

## Без Docker

Linux одной командой: `curl -fsSL https://raw.githubusercontent.com/torwin-job/musik/main/scripts/setup-linux.sh | bash`.
Скрипт ставит зависимости, создаёт `.venv`, `.env`, выполняет `musik db migrate`
и запускает сервисы (`scripts/musik.sh start|stop|restart|status|logs`).
Windows: `scripts/setup-windows.ps1`.

Вручную:

```bash
export MUSIK_DATABASE_URL="sqlite:///$PWD/data/db/musik.db" MUSIK_LIBRARY=/path/to/music
export MUSIK_PASSWORD=… MUSIK_API_TOKEN=… MUSIK_SESSION_SECRET=…
musik db migrate
musik worker &                 # фоновые задания
make player && ./player/bin/musik-player
```

`musik db migrate` создаёт новую базу или обновляет существующую (SQLite v5 и
новее) до текущей схемы. Player и worker с неподходящей схемой не стартуют и
просят выполнить миграцию.

### PostgreSQL без Docker

Создайте пустую базу и перенесите в неё SQLite после остановки player и worker:

```bash
musik db transfer --source data/db/musik.db \
  --destination "postgresql://musik:…@db.example.net/musik?sslmode=require" \
  --report musik-transfer-report.json
```

Команда не изменяет исходный файл: делает снимок, проверяет его, копирует
строки и сверяет число строк и SHA-256 каждой таблицы. После успешного отчёта
укажите тот же URL в `MUSIK_DATABASE_URL` для player и worker. Аудиофайлы и
кеши база не переносит.

Подробнее: [API.md](API.md) · [MOBILE.md](MOBILE.md) · [CAPACITY.md](CAPACITY.md)
(ресурсы под 50 тыс. треков).
