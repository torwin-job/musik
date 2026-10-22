# Обновление установки до новой версии

Инструкция для установки из `torwin-job/musik` (схема базы v5), запущенной
через Docker Compose. Шаги идут по порядку; после каждого установка рабочая,
и на любом можно остановиться:

1. [Новая версия: готовые образы или своя сборка](#1-новая-версия)
2. [Базовое обновление на SQLite](#2-базовое-обновление-на-sqlite) — обязательный шаг
3. [Несколько пользователей](#3-несколько-пользователей) — по желанию
4. [Вход через OIDC](#4-вход-через-oidc) — по желанию, после шага 3
5. [Переезд на PostgreSQL](#5-переезд-на-postgresql) — по желанию

Установка без Docker — в [конце](#без-docker). Все переменные `.env` описаны в
[CONFIG.md](CONFIG.md), установка с нуля — в [DEPLOY.md](DEPLOY.md).

Что сохраняется: библиотека, эмбеддинги (пересчитывать не нужно), история
прослушиваний, лайки, избранное, «Потом», плейлисты, правила радио и темы.
Новая версия переносит всё это в профиль «Main» владельца установки.

Команды выполняются из каталога репозитория. Если Compose-проект запускался
с `-p <имя>` или с дополнительными файлами `-f`, передавайте те же параметры
скрипту обновления: `./scripts/update-compose.sh -p <имя> -f docker-compose.yml`.

## 0. Перед началом

- Сохраните копию `.env`: `cp .env .env.before-upgrade`.
- Убедитесь, что в каталоге с базой есть место ещё на одну её копию:
  перед изменением схемы сервис `migrate` сам делает и проверяет резервную
  копию в `/data/backups` (том `musik-data`).
- Остановите всё, что пишет в базу помимо Compose (свои cron-задачи, `musik`
  на хосте). Плеер и воркер скрипт обновления остановит сам.

## 1. Новая версия

Получите новый код (в нём новые Compose-файлы и скрипт обновления):

```bash
git pull
```

Дальше один из двух вариантов.

**Готовые образы.** Ничего не компилируется на сервере. В `.env` можно
закрепить сборку (по умолчанию `latest` — последняя версия `main`):

```dotenv
MUSIK_IMAGE_OWNER=torwin-job
MUSIK_IMAGE_TAG=latest
```

Обновление на шаге 2 запускается командой `./scripts/update-compose.sh`.
Скрипт скачивает образы player, worker и migrate и проверяет, что все три
собраны из одного коммита.

**Своя сборка.** Нужны Docker Buildx (пакет `docker-buildx` или
`docker-buildx-plugin`; проверка — `docker buildx version`) и около 10 ГБ
свободного места (PyTorch для воркера). Без Buildx Compose собирает старым
сборщиком, и образ воркера падает на `--mount option requires BuildKit`. Обновление на шаге 2 запускается командой
`./scripts/update-compose.sh --build`. Скрипт соберёт все три образа из
текущего checkout. Если раньше вы запускали `docker compose up` без `-f`,
это ваш вариант.

В обоих случаях сначала скачиваются или собираются образы, и только потом
останавливается работающая старая версия.

## 2. Базовое обновление на SQLite

База остаётся SQLite, вход — прежний пароль. В `.env` обязательных изменений
нет: `MUSIK_PASSWORD`, `MUSIK_API_TOKEN`, `MUSIK_SESSION_SECRET` и
`MUSIK_LIBRARY` остаются как были. Можно добавить:

```dotenv
# Веб-интерфейс по умолчанию: original, mozgolika или rainedev.
# Каждый браузер может переключить его в «Профиль → Интерфейс».
MUSIK_UI=original
```

Не задавайте `MUSIK_DB_PATH` и `MUSIK_DATABASE_URL`: Compose сам указывает на
прежнюю базу `/data/db/musik.db`.

Запустите обновление:

```bash
./scripts/update-compose.sh            # готовые образы
./scripts/update-compose.sh --build    # своя сборка
```

Скрипт останавливает player и worker, запускает `migrate` (резервная копия,
проверка копии, миграция схемы v5 → v14), затем запускает worker и player и
ждёт их healthcheck. Успешный конец выглядит так:

```text
Update complete; worker and player are healthy.
```

Проверьте:

```bash
docker compose logs migrate | tail -3    # Verified pre-migration backup …, Database ready: schema v14
curl -s http://127.0.0.1:8787/api/health # "ok":true
```

Войдите в веб-интерфейс прежним паролем: история, избранное и плейлисты на
месте. Чтобы новые возможности (сборники, соавторы, фото артистов) применились
к уже отсканированной библиотеке, запустите пересканирование: «Загрузка →
Пересканировать» или `MUSIK_API_TOKEN=<токен> make rescan`. Повторный скан перечитывает теги один раз
и не пересчитывает эмбеддинги.

Если `migrate` завершился ошибкой, player и worker остаются остановленными,
база не изменена. Причина — в `docker compose logs migrate`; после исправления
запустите скрипт снова.

## 3. Несколько пользователей

Каждый пользователь получает аккаунт и свои профили: историю, избранное,
плейлисты и настройки радио. Библиотека общая. Ваши прежние данные останутся у
первого администратора — им станете вы.

**Нужен HTTPS.** Cookie сессии ставятся с флагом Secure, поэтому сервер должен
открываться по `https://`. Поставьте перед player reverse proxy, например Caddy
(сам получает сертификат Let's Encrypt):

```caddyfile
music.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

Измените `.env`:

```dotenv
# Удалите или очистите общий пароль и токен: в этом режиме они запрещены.
MUSIK_PASSWORD=
MUSIK_API_TOKEN=
# MUSIK_AUTH_DISABLED тоже не задавать.

MUSIK_MULTI_USER=1
MUSIK_PUBLIC_BASE_URL=https://music.example.com
MUSIK_SECURE_COOKIE=1
# Одноразовое приглашение для первого администратора: openssl rand -hex 32
MUSIK_BOOTSTRAP_INVITE=<случайная строка не короче 32 символов>
```

`MUSIK_SESSION_SECRET` оставьте. Примените настройки:

```bash
./scripts/update-compose.sh            # или --build
```

`migrate` создаст из `MUSIK_BOOTSTRAP_INVITE` приглашение на 24 часа. Откройте

```text
https://music.example.com/?invitation=<значение MUSIK_BOOTSTRAP_INVITE>
```

придумайте имя и пароль (не короче 8 символов) и нажмите «Создать аккаунт».
Вы войдёте администратором со всеми прежними данными. После этого удалите
`MUSIK_BOOTSTRAP_INVITE` из `.env`: приглашение одноразовое.

Других пользователей приглашает администратор: «Профиль → Пригласить
пользователя» создаёт ссылку `…/?invitation=…`; по ней человек сам задаёт имя и
пароль. В «Профиле» каждый может сменить пароль, создать дополнительные
профили и переключаться между ними.

**Телефон.** Приложение входит одним из способов:

- по имени и паролю: `POST /api/auth/token` с
  `{"username", "password", "device_name"}` возвращает токен устройства
  (см. [API.md](API.md));
- по QR: «Профиль → Устройства и токены → Создать токен» показывает секрет
  и QR `musik://connect`, который приложение сканирует.

Токен привязан к одному профилю и действует 180 дней. Прежний общий
`MUSIK_API_TOKEN` в этом режиме не работает: выдайте телефонам новые токены.

## 4. Вход через OIDC

OIDC (Keycloak, Authentik, Authelia, Google и т. п.) — второй способ входа
рядом с паролем. Создайте в провайдере OIDC-клиента с redirect URI

```text
https://music.example.com/api/auth/oidc/default/callback
```

и добавьте в `.env`:

```dotenv
MUSIK_OIDC_ISSUER=https://identity.example.com/realms/music
MUSIK_OIDC_CLIENT_ID=musik
MUSIK_OIDC_CLIENT_SECRET=<секрет клиента; для публичного клиента пусто>
```

Примените: `./scripts/update-compose.sh` (или `--build`). Player проверяет
провайдера при старте; если issuer недоступен, player не запустится —
смотрите `docker compose logs player`.

На экране входа появится кнопка «Войти через аккаунт». Уже зарегистрированный
пользователь подключает OIDC к своему аккаунту в «Профиль → Подключить ещё один
OIDC-вход». Новый пользователь по ссылке-приглашению может выбрать пароль или
OIDC.

## 5. Переезд на PostgreSQL

Необязательный шаг для больших библиотек и нескольких пользователей. Compose
поднимает PostgreSQL 17 в отдельном томе `musik-postgres` без открытого наружу
порта. Данные переносятся из SQLite автоматически, с проверкой числа строк и
контрольных сумм по каждой таблице. Файл SQLite не изменяется и остаётся
резервной копией.

Добавьте в `.env`:

```dotenv
COMPOSE_PROFILES=postgres
MUSIK_POSTGRES_USER=musik
MUSIK_POSTGRES_DB=musik
# Только [0-9a-f]: пароль входит в URL. openssl rand -hex 32
MUSIK_POSTGRES_PASSWORD=<пароль>
MUSIK_DATABASE_URL=postgresql://musik:<тот же пароль>@postgres:5432/musik?sslmode=disable
MUSIK_IMPORT_SQLITE=1
```

Запустите `./scripts/update-compose.sh` (или `--build`). В логе `migrate`
появятся строки `SQLite import verified: <имя>-transfer.json` и
`Database ready: schema v14, backend=postgresql`. Отчёт о переносе лежит в
`/data/backups`.

После проверки уберите `MUSIK_IMPORT_SQLITE=1` из `.env`. Повторный импорт той
же базы безопасен: `migrate` помнит, что она уже перенесена, и ничего не
копирует второй раз. Пароль PostgreSQL задаётся только при создании тома;
смена `MUSIK_POSTGRES_PASSWORD` позже не меняет пароль в базе.

Резервная копия PostgreSQL:
`docker compose exec postgres pg_dump -U musik -Fc musik > musik.dump`.

## Откат

Миграции необратимы, поэтому откат — это возврат к резервной копии:

1. `docker compose stop player worker`.
2. Верните прежний код (`git checkout <прежний коммит>`) и `.env.before-upgrade`.
3. Скопируйте файл из `/data/backups` (последний `*.sqlite` перед миграцией)
   на место `/data/db/musik.db`, удалив рядом `musik.db-wal` и `musik.db-shm`.
4. Запустите прежнюю версию: `docker compose up -d`.

Прослушивания после момента копии при откате теряются.

## Без Docker

Для установки через `scripts/setup-linux.sh` достаточно запустить его снова:
он обновит код, зависимости и выполнит миграцию. Вручную:

```bash
scripts/musik.sh stop
git pull
.venv/bin/pip install -e .
.venv/bin/musik db migrate          # резервную копию базы сделайте заранее
make player
scripts/musik.sh start
```

Переменные из шагов 3–5 задаются в том же `.env`. Перенос в PostgreSQL без
Docker — командой `musik db transfer`, см. [DEPLOY.md](DEPLOY.md#postgresql-без-docker).
