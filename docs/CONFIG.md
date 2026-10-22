# Настройка `.env`

Compose читает `.env` в корне репозитория и передаёт его player, worker и
migrate. Начните с примера и заполните по порядку разделы 1–3; остальное —
по необходимости.

```bash
cp .env.example .env
openssl rand -hex 32     # генератор для паролей, токенов и секретов
```

Правила:

- Значения без кавычек: `MUSIK_PASSWORD=abc`, не `"abc"`.
- После изменения `.env` примените его: `./scripts/update-compose.sh`
  (или `--build` для своей сборки). Скрипт перезапустит сервисы и, если нужно,
  подготовит базу.
- `.env` содержит секреты: не коммитьте его и не публикуйте в логах.

## 1. Библиотека

| Переменная | Значение |
| --- | --- |
| `MUSIK_LIBRARY` | Путь к музыке **на хосте**. В контейнерах она видна как `/music`; worker читает её только для чтения. |

## 2. Вход: один владелец (по умолчанию)

Один пароль для веб-интерфейса и один токен для API и телефона.

| Переменная | Значение |
| --- | --- |
| `MUSIK_PASSWORD` | Пароль входа в веб-интерфейс. |
| `MUSIK_API_TOKEN` | Bearer-токен для API, приложений и `make rescan`. Телефон также может получить его по паролю: `POST /api/auth/token`. |
| `MUSIK_SESSION_SECRET` | Ключ подписи cookie сессий. Нужен в обоих режимах; при смене все выходят из аккаунтов. |
| `MUSIK_SECURE_COOKIE=1` | Ставить, когда сайт открывается по HTTPS. |
| `MUSIK_AUTH_DISABLED=1` | Вход без пароля. Только для локальной сети, никогда для публичного адреса. |
| `MUSIK_CORS_ORIGINS` | Список разрешённых чужих origin для браузерных клиентов; пусто — только свой сайт. |

Player не запустится без `MUSIK_PASSWORD` или `MUSIK_API_TOKEN`, если не
задан `MUSIK_AUTH_DISABLED=1` или режим нескольких пользователей.

## 3. Вход: несколько пользователей

Включается вместо раздела 2. Требует HTTPS.

| Переменная | Значение |
| --- | --- |
| `MUSIK_MULTI_USER=1` | Включает аккаунты, профили и приглашения. `MUSIK_PASSWORD`, `MUSIK_API_TOKEN` и `MUSIK_AUTH_DISABLED` при этом должны быть пустыми. |
| `MUSIK_PUBLIC_BASE_URL` | Публичный адрес `https://…` без пути. Из него строятся ссылки-приглашения и QR. |
| `MUSIK_AUTH_ALLOWED_ORIGINS` | Дополнительные HTTPS-адреса того же веб-интерфейса через запятую. Пусто — разрешён только публичный адрес. |
| `MUSIK_SECURE_COOKIE=1` | Обязательно вместе с HTTPS. |
| `MUSIK_SESSION_SECRET` | Как в разделе 2. |
| `MUSIK_BOOTSTRAP_INVITE` | Одноразовый секрет первого администратора, не короче 32 символов. `migrate` превращает его в приглашение на 24 часа: `https://…/?invitation=<секрет>`. После входа удалить из `.env`. |
| `MUSIK_CORS_ORIGINS` | Пусто или ровно `MUSIK_PUBLIC_BASE_URL`. |

Если сервер открывается по публичному и домашнему доменам, оставьте публичный адрес в `MUSIK_PUBLIC_BASE_URL`, а дополнительные адреса перечислите явно:

```dotenv
MUSIK_PUBLIC_BASE_URL=https://music.example.com
MUSIK_AUTH_ALLOWED_ORIGINS=https://music.home.example,https://music.vpn.example:8443
```

Это разрешает вход, регистрацию, выдачу токена и изменения через cookie-сессию с указанных адресов. Origin сравнивается целиком, включая порт; HTTP, пути, параметры запроса, фрагменты, имя пользователя в URL и маски доменов не допускаются. Некорректные дополнительные адреса игнорируются. Сервер должен быть доступен по этим именам через настроенный HTTPS-прокси.

Настройка не добавляет разрешений CORS и не отключает CSRF: для изменений в cookie-сессии по-прежнему нужен действительный CSRF-токен. Вход через OIDC продолжает использовать callback на публичном адресе.

### OIDC (необязательно, поверх раздела 3)

| Переменная | Значение |
| --- | --- |
| `MUSIK_OIDC_ISSUER` | URL провайдера `https://…` (issuer из `.well-known/openid-configuration`). Пусто — только вход по паролю. |
| `MUSIK_OIDC_CLIENT_ID` | ID клиента. Обязателен, если задан issuer. |
| `MUSIK_OIDC_CLIENT_SECRET` | Секрет клиента; пусто для публичного клиента с PKCE. |

Redirect URI клиента: `<MUSIK_PUBLIC_BASE_URL>/api/auth/oidc/default/callback`.

## 4. База данных

По умолчанию SQLite в томе `musik-data` (`/data/db/musik.db`); ничего задавать
не нужно.

| Переменная | Значение |
| --- | --- |
| `MUSIK_DATABASE_URL` | `sqlite:////data/db/musik.db` (по умолчанию) или `postgresql://user:pass@host:5432/db?sslmode=…`. |
| `MUSIK_DB_PATH` | Устаревший путь к SQLite. Не задавать вместе с `MUSIK_DATABASE_URL`; в Compose не нужен. |

### Встроенный PostgreSQL в Compose

| Переменная | Значение |
| --- | --- |
| `COMPOSE_PROFILES=postgres` | Запускает контейнер `postgres` (порт наружу не открыт, данные в томе `musik-postgres`). |
| `MUSIK_POSTGRES_USER`, `MUSIK_POSTGRES_DB` | Пользователь и база, по умолчанию `musik`. |
| `MUSIK_POSTGRES_PASSWORD` | Пароль, только `[0-9a-f]` (`openssl rand -hex 32`): он подставляется в URL. Применяется только при создании тома. |
| `MUSIK_DATABASE_URL` | `postgresql://musik:<пароль>@postgres:5432/musik?sslmode=disable` (внутренняя сеть Compose). Для внешнего сервера — его адрес и `sslmode=require`. |
| `MUSIK_IMPORT_SQLITE=1` | Перенести существующую SQLite в пустой PostgreSQL при следующем обновлении. Повтор безопасен; после переноса убрать. |
| `MUSIK_IMPORT_SQLITE_SOURCE` | Путь к SQLite внутри контейнера, по умолчанию `/data/db/musik.db`. |

`MUSIK_WRITERS_STOPPED` ставит скрипт обновления; вручную не задавать.

## 5. Образы

| Переменная | Значение |
| --- | --- |
| `MUSIK_IMAGE_OWNER` | Чьи образы брать из GHCR, по умолчанию `torwin-job`. |
| `MUSIK_IMAGE_TAG` | Сборка: `latest` (последний `main`), `v1.2.3`, `sha-<коммит>`. Для своей сборки — любое имя, по умолчанию `local`. |

## 6. Интерфейс, ссылки и телефон

| Переменная | Значение |
| --- | --- |
| `MUSIK_UI` | Интерфейс по умолчанию: `original`, `mozgolika` или `rainedev`. Браузер переключает его в «Профиль → Интерфейс». |
| `MUSIK_PUBLIC_BASE_URL` | Также нужен в однопользовательском режиме для ссылок «поделиться радио» и QR. |
| `MUSIK_MOBILE_FORMAT`, `MUSIK_MOBILE_BITRATE` | Перекодирование для телефона: `aac`/`mp3`, по умолчанию `aac`, `160k`. |
| `MUSIK_SHARE_BITRATE`, `MUSIK_SHARE_MAX_LISTENERS` | Поток «поделиться радио»: `192k`, 4 слушателя. |
| `MUSIK_FFMPEG` | Путь к ffmpeg, по умолчанию `ffmpeg`. |

## 7. Радио и рекомендации

Значения по умолчанию подходят большинству; меняйте по одному.

| Переменная | По умолчанию | Смысл |
| --- | --- | --- |
| `MUSIK_QUEUE_SIZE` | 6 | Треков в очереди радио. |
| `MUSIK_TASTE_ALPHA` | 0.22 | Скорость подстройки вкуса под последние лайки. |
| `MUSIK_PROFILE_FORMING_AT`, `MUSIK_PROFILE_READY_AT` | 3, 8 | Сколько сигналов нужно, чтобы профиль вкуса считался формирующимся и готовым. |
| `MUSIK_EXPLORE_RATIO` | 0.15 | Доля «нового» в очереди. |
| `MUSIK_EXPLORE_LO`, `MUSIK_EXPLORE_HI` | 0.10, 0.40 | Границы этой доли. |
| `MUSIK_DISCOVER_EXPLORE` | 0.35 | Доля нового в «Открытиях». |
| `MUSIK_NEW_TRACK_DAYS`, `MUSIK_NEW_ALBUM_DAYS` | 14, 14 | Сколько дней трек и альбом считаются новыми. |

## 8. Worker

| Переменная | По умолчанию | Смысл |
| --- | --- | --- |
| `MUSIK_WORKERS` | 4 | Параллельный разбор файлов при скане. |
| `MUSIK_EMBED_WORKERS` | 1 | Параллельная подготовка аудио для CLAP. |
| `MUSIK_EMBED_SPAN_SEC`, `MUSIK_EMBED_SEGMENT_SEC` | 30, 10 | Длина трёх фрагментов трека и окно модели. |
| `MUSIK_NIGHTLY_MIXES_ENABLED` | 1 | Пересобирать миксы каждую ночь. |
| `MUSIK_NIGHTLY_MIXES_HOUR` | 3 | Час пересборки. |
| `MUSIK_NIGHTLY_MIXES_TIMEZONE` | `UTC` в Compose | IANA-зона, например `Europe/Moscow`. |
| `MUSIK_WATCH_DEBOUNCE_SEC`, `MUSIK_WATCH_CLUSTERS`, `MUSIK_WATCH_MIXES` | 45, 1, 1 | Для `musik watch`: пауза после изменений в папке и что пересчитывать. |
| `MUSIK_WORKER_JOB_KINDS` | все | Виды заданий этого воркера, JSON-список, например `["embed","full_rescan"]`, если работают несколько воркеров (отдельный GPU). |

## Пример: один владелец, SQLite

```dotenv
MUSIK_LIBRARY=/srv/music
MUSIK_PASSWORD=<openssl rand -hex 16>
MUSIK_API_TOKEN=<openssl rand -hex 32>
MUSIK_SESSION_SECRET=<openssl rand -hex 32>
```

## Пример: несколько пользователей, OIDC, PostgreSQL

```dotenv
MUSIK_LIBRARY=/srv/music
MUSIK_SESSION_SECRET=<openssl rand -hex 32>
MUSIK_MULTI_USER=1
MUSIK_PUBLIC_BASE_URL=https://music.example.com
MUSIK_SECURE_COOKIE=1
MUSIK_BOOTSTRAP_INVITE=<openssl rand -hex 32; удалить после первого входа>
MUSIK_OIDC_ISSUER=https://identity.example.com/realms/music
MUSIK_OIDC_CLIENT_ID=musik
MUSIK_OIDC_CLIENT_SECRET=<секрет клиента>
COMPOSE_PROFILES=postgres
MUSIK_POSTGRES_PASSWORD=<openssl rand -hex 32>
MUSIK_DATABASE_URL=postgresql://musik:<тот же пароль>@postgres:5432/musik?sslmode=disable
```
