# musik API

Base URL: `http://127.0.0.1:8787`  
Машиночитаемая спецификация: [`GET /api/openapi.json`](/api/openapi.json). Её источник — `player/internal/api/openapi.json`; `docs/openapi.yaml` — тот же документ. Тест `TestEmbeddedOpenAPIMatchesRegisteredRoutes` сверяет пути с таблицей `apiRoutes`.

JSON, кроме стрима, обложек, фото и `POST /api/library/upload`. Ошибка: `{"error":"…","code":"…"}`.

Маршрут принадлежит одному scope. Middleware читает эту таблицу, а не отдельный список префиксов.

| Scope | Кто | Что |
|-------|-----|-----|
| `public` | без сессии | health, вход, манифест, чужой MP3-эфир |
| `user` | вошедший аккаунт | пароль, свои профили, свои токены устройств |
| `profile` | активный профиль слушателя | каталог, радио, вкус, плейлисты, своя статистика |
| `admin` | администратор установки | библиотека, задания, плагины, настройки, приглашения, учётки |

В режиме одного пароля cookie или `Authorization: Bearer <MUSIK_API_TOKEN>` открывают `profile` и `admin`. `MUSIK_AUTH_DISABLED=1` выключает проверку и не предназначен для публичного адреса. Логин: не больше 5 попыток в минуту с одного IP.

Режим аккаунтов (`MUSIK_MULTI_USER=1`, HTTPS `MUSIK_PUBLIC_BASE_URL`) не принимает общий пароль, общий API-токен и `MUSIK_AUTH_DISABLED`. Изменяющие запросы из браузера несут cookie, `X-CSRF-Token` и тот же `Origin`, что публичный URL. Cookie и `Authorization` вместе — `400 credential_conflict`. Токен устройства ходит только в `profile`, плюс `POST /api/jobs/mix_pack` и `GET /api/jobs/{id}` своего задания. Чужой `session_id` — `404`.

## public

| Метод | Путь | Что делает |
|-------|------|------------|
| GET | `/api/health` | `{ok, version, api_version, auth}`; с авторизацией ещё `tracks` и `dim` |
| GET | `/api/themes` | встроенные темы и пакеты из каталога тем |
| GET | `/api/openapi.json` | эта спецификация |
| GET | `/api/auth/me` | без сессии и для токена устройства: `{ok, auth_enabled}`; неверный Bearer здесь `200` и `ok:false`. Браузерная сессия аккаунта: пользователь, роли, профили |
| POST | `/api/auth/login` | один пароль: `{password}` → cookie ~14 дней. Аккаунты: `{username, password}` |
| POST | `/api/auth/register` | `{invitation, username, password}` — новый локальный аккаунт и сразу вход. Имя 1–64 символа, пароль 8–256. В режиме одного пароля маршрута нет: `404` |
| POST | `/api/auth/token` | телефон. Аккаунты: `{username, password, device_name, profile_id?}` → токен устройства на 180 дней. Один пароль: `{password}` → `MUSIK_API_TOKEN` |
| POST | `/api/auth/logout` | сброс cookie. У вошедшего аккаунта нужен CSRF |
| GET | `/api/auth/oidc/default/start` | старт OIDC, Authorization Code + PKCE |
| GET | `/api/auth/oidc/default/callback` | ответ провайдера → серверная сессия |
| GET | `/listen/{token}` | непрерывный MP3. Принимается и `/listen/{token}.mp3`. Слушатель не пишет вкус и статистику владельца ссылки |
| GET | `/manifest.webmanifest` | манифест PWA |

## user

Только сессия аккаунта, не токен устройства.

| Метод | Путь | Что делает |
|-------|------|------------|
| POST | `/api/account/password` | `{current, new}` — смена пароля текущего локального аккаунта |
| POST | `/api/auth/oidc/default/link` | начать привязку другого OIDC-входа |
| GET | `/api/profiles` | профили этого пользователя |
| POST | `/api/profiles` | `{name}` — новый профиль |
| PATCH | `/api/profiles/{profile_id}` | переименовать свой профиль |
| DELETE | `/api/profiles/{profile_id}` | мягко удалить свой профиль, если он не последний |
| POST | `/api/profiles/{profile_id}/activate` | сделать профиль активным в этой сессии |
| GET | `/api/account/device-tokens` | метаданные токенов; секрета в ответе нет |
| POST | `/api/account/device-tokens` | `{name, profile_id?}` — секрет показывается один раз |
| DELETE | `/api/account/device-tokens/{id}` | отозвать свой токен |
| POST | `/api/account/device-tokens/{id}/replace` | отозвать и выпустить новый секрет на тот же профиль |

## profile

### Каталог и файлы

| Метод | Путь | Что делает |
|-------|------|------------|
| GET | `/api/library` | плоский список. `?artist=` `?album=` `?limit=` (потолок 2000). У строки есть `plays`, `early_skips`, `last_played_at`. Сортировку «чаще / давно не играло / ни разу» делает интерфейс |
| GET | `/api/artists` | `{artists, count}`. `?limit=` до 500 |
| GET | `/api/albums` | `{albums, count}`. `?limit=` до 500 |
| GET | `/api/tracks/{id}` | трек и `passport`. Нет трека — `404` |
| GET | `/api/tracks/{id}/lyrics` | текст, если он уже лежит в базе |
| GET | `/api/stream/{id}` | файл, с `Range`. `?q=mobile\|aac\|mp3` или `?fmt=` — мобильный транскод |
| GET | `/api/artwork/{id}` | JPEG 96/256/640 (`?w=`, по умолчанию 640). `?full=1` — оригинал |
| GET | `/api/artist-photos` | имя артиста → URL фото |
| GET | `/api/artist-photos/{id}` | JPEG. `?w=96\|256\|640`, `?full=1` — оригинал |
| GET | `/api/similar/{id}` | 10 ближайших по косинусу |
| GET | `/api/similar/artists` | похожие артисты |
| GET | `/api/similar/albums` | похожие альбомы |

Строка библиотеки в интерфейсе: число `plays`; «ни разу», если их нет; «часто бросаешь», если запусков не меньше трёх и ранних скипов не меньше половины.

### Паспорт трека

`passport` есть только у `GET /api/tracks/{id}`. Это один запрос по id: файл, `features`, строка `track_stats` этого профиля и `track_preferences`. Поля пропадают, если значения нет.

| Поле | Смысл |
|------|--------|
| `plays`, `finishes`, `early_skips` | запуски, дослушивания, ранние скипы |
| `last_played_at` | последний запуск |
| `rating` | `like` или `dislike`. Нейтральная оценка в ответ не входит |
| `year`, `duration`, `genres` | год, длительность, жанры файла |
| `lufs`, `bpm`, `key`, `mode` | измеренные признаки. `mode` — `major` или `minor` |
| `format`, `bitrate`, `sample_rate`, `channels` | контейнер и параметры файла |

Отдельной шкалы звёзд нет: человеку видны лайк, дизлайк и доля дослушиваний.

### Сессия и радио

`session_id` приходит из старта радио, `/api/play`, `/api/session/start` или микса. Сессии лежат в базе и переживают перезапуск плеера. `GET /api/now` и прыжки принимают id в query, теле `session_id` или заголовке `X-Session-Id`.

| Метод | Путь | Что делает |
|-------|------|------------|
| POST | `/api/radio/start` | радио. Тело ниже |
| POST | `/api/session/start` | `{seed_track_id?}`. Режим `session`: без радио-исключений и без якоря |
| POST | `/api/session/jump` | `{session_id, index? \| track_id}` в фиксированном списке |
| POST | `/api/session/back` | предыдущий трек списка или радио |
| GET | `/api/now` | current, queue, имя, режим. `?session_id=` |
| POST | `/api/play` | фиксированный список: `{track_id \| track_ids \| artist \| album, start_index?, start_track_id?, name?}` |
| GET | `/api/playback/state` | где остановились: сессия, трек, позиция |
| PUT | `/api/playback/state` | записать позицию с устройства, которое сейчас играет |
| POST | `/api/events` | события прослушивания |

`POST /api/radio/start` зовёт тот же `Start("radio", seed)`, не второй движок. Поля:

| Поле | Смысл |
|------|--------|
| `seed_track_id` | якорь-трек. Он становится текущим, если его не закрыло жёсткое правило |
| `seed_artist` | якорь-артист, вектор — центроид его треков |
| `seed_album` | якорь-альбом. Если задан альбом, артист сам по себе якорем не становится |
| `station` | сейчас только `forgotten` |
| `proximity` | `closer` или `wider` — один шаг на уже открытой сессии |
| `session_id` | нужен вместе с `proximity`, чтобы не открывать новую сессию |
| `context_ids` | включить контексты на старте |
| `transition_profile` | профиль переходов, если это новый старт, а не подстройка |

Якорь в ответе — `anchor`: `kind` (`track`, `artist`, `album`, `forgotten`), `track_id`, `artist`, `album`, `label`, `proximity` (`near`, `close`, `wide`). Пока близость не `wide`, `name` равен «радио от: …». На `wide` имя снова «радио», но якорь в сессии остаётся, и «ближе» может вернуть его в смесь. Обычный старт без семени — `anchor: null`.

Старт — `near` (якорь и вкус поровну). «Ближе» с `wide` даёт `near`, с `near` — `close` (якорь доминирует). «Шире» спускает на шаг и на `wide` оставляет обычный вкус. Подстройка случается, только если `session_id` указывает на радио, у которого якорь уже есть. Иначе открывается новая сессия.

`next_source` — ключ источника первого трека очереди. Тот же ключ лежит в `source` у current и элементов queue, рядом с `request_id` и `impression_id`. Интерфейс переводит ключи так:

| `source` | Подпись |
|----------|---------|
| `exploit` | похоже на тебя |
| `transition` | часто после этой |
| `explore`, `explore_adjacent` | чуть в сторону |
| `resurface` | давно не звучало |
| `new_in_library` | новое в библиотеке |
| `wildcard` | для разнообразия |
| `manual` | ты выбрал |
| `radio_start` | старт радио |
| `refill` | добор очереди |

Станция `forgotten` якорится на треки с не меньше чем тремя дослушиваниями и последним запуском старше 60 дней. Если таких нет — `409`, код `empty_station`. Жёсткий block и cooldown не обходятся: ключ правила исключает цель, фильтр остаётся в `BuildCore`.

`POST /api/playlists/{id}/radio` стартует радио от первого трека плейлиста, то есть с якорем-треком. Отдельных методов «ещё такое» и «сохранить станцию» нет.

Очередь — одно ядро: кандидаты, признаки, оценка, выбор. Доля нового сначала статическая. Thompson sampling включается после минимума разрешённых исходов на источник и остаётся внутри границ `explore_lo` / `explore_hi`.

Зрелость: `discovering`, пока положительных сигналов меньше `MUSIK_PROFILE_FORMING_AT` (по умолчанию 3); `forming` — до `MUSIK_PROFILE_READY_AT` (по умолчанию 8); дальше `ready`.

### События

```json
{
  "type": "track_start|progress|track_end|skip|like|dislike",
  "event_id": "стабильный id повтора, можно не присылать",
  "impression_id": "32 hex из current или queue",
  "client_id": "id браузера",
  "device_id": "имя устройства",
  "track_id": 83,
  "session_id": "...",
  "position_sec": 12.5,
  "duration_sec": 240,
  "listened_sec": 12.5,
  "reason": "completed|skipped|next"
}
```

Обязательны `type`, `track_id`, `session_id`. Новый клиент повторяет тот же `event_id` и возвращает `impression_id`. Клиент без них получает id от сервера; для старого события ищется единственный pending impression пары `(session_id, track_id)`. Несколько подходящих — `409 ambiguous_impression`. Повтор start, end или skip не двигает impression и `track_stats`. Лайк и дизлайк пишутся в `track_preferences` и кормят вкус. Ответ skip и track_end: `{ok, next, queue, maturity, signed_weight, session_id, …}`.

### Полка, избранное, рекомендации

Полка — только виды из `player/internal/playback/mixes.json`: `for_you`, `daily`, `favorites`, `later`, `new_releases`, `weekly` и `weekday_mon` … `weekday_sun`.

| Метод | Путь | Что делает |
|-------|------|------------|
| GET | `/api/mixes` | полка |
| POST | `/api/mixes/{kind}/play` | играть этот вид, включая дневной микс |
| GET | `/api/later` | «потом» |
| POST | `/api/later` | `{track_id}` |
| DELETE | `/api/later` | `{track_id}` |
| GET | `/api/favorites` | треки, артисты, альбомы |
| POST | `/api/favorites` | добавить. `{type: track\|artist\|album, track_id?, artist?, album?}` |
| DELETE | `/api/favorites` | убрать, то же тело |
| POST | `/api/favorites/toggle` | переключить |
| GET | `/api/favorites/status` | `?type=track\|artist\|album` и id или имя → `{favorited}` |
| GET | `/api/recommend/seed` | `?type=` и `track_id`, `artist` или `album` — похожие треки |
| GET | `/api/recommend/favorites` | микс по любимым трекам, артистам и альбомам. Лайкнутое вне топа похожих тоже может попасть. Ответ: `tracks`, `artists`, `albums`, `based_on`, `explanation`; пустое избранное — `empty: true` и `hint` |
| GET | `/api/discover/albums` | подсказки новых альбомов |
| GET | `/api/discover/resurfaced` | давно не открытый каталог |

### Плейлисты и метки

Умный плейлист — тот же `POST /api/playlists` с `type: smart` и `rule_json`. Нового вида полки нет. Правило, `schema_version: 1`:

- поля: `tag`, `artist`, `album`, `liked`, `plays`, `last_played`, `year`, `bpm`, `lufs`, `duration`;
- операции: `eq`, `neq`, `in`, `not_in`, `gt`, `gte`, `lt`, `lte`, `between`, `older_than_days`, `newer_than_days`;
- сортировка: `plays`, `plays_desc`, `last_played`, `year_desc`, `bpm`.

Интерфейс создаёт два таких правила. «Часто слушаю»: `plays >= 3`, сортировка `plays_desc`. «Забытое»: `last_played` старше 60 дней и `plays >= 1`, сортировка `last_played`. Это не станция `forgotten`: та смотрит на дослушивания, а не на число запусков.

| Метод | Путь | Что делает |
|-------|------|------------|
| GET | `/api/playlists` | свои и умные. `?type=` через запятую, `?all=1` — с архивными |
| POST | `/api/playlists` | создать. Для умного — `rule_json` |
| GET | `/api/playlists/{id}` | один плейлист |
| PATCH | `/api/playlists/{id}` | имя, правило и прочие правки |
| DELETE | `/api/playlists/{id}` | в архив. `?hard=1` удаляет |
| POST | `/api/playlists/import` | M3U или JSON. Путь на сервере не принимается |
| POST | `/api/playlists/preview` | `{rule_json}` — какие id выйдут, без записи |
| POST | `/api/playlists/from-favorites` | скопировать избранное в новый плейлист |
| POST | `/api/playlists/from-later` | то же для «потом» |
| POST | `/api/playlists/{id}/tracks` | добавить трек |
| DELETE | `/api/playlists/{id}/tracks/{item_id}` | убрать пункт |
| POST | `/api/playlists/{id}/reorder` | новый порядок |
| POST | `/api/playlists/{id}/duplicate` | копия |
| POST | `/api/playlists/{id}/from-queue` | дописать текущую очередь. `{session_id}` |
| POST | `/api/playlists/{id}/play` | играть с позиции |
| POST | `/api/playlists/{id}/radio` | радио от первого трека |
| GET | `/api/playlists/{id}/export` | JSON, или M3U при `?format=m3u` |
| GET | `/api/tags` | свои метки |
| POST | `/api/tags` | создать метку |
| POST | `/api/tags/{id}/tracks` | повесить метку на трек |
| DELETE | `/api/tags/{id}/tracks` | снять метку |

### Вкус, правила, статистика

| Метод | Путь | Что делает |
|-------|------|------------|
| GET | `/api/status` | число треков, зрелость, режим, сессия, explore, `db_backend` (`sqlite` или `postgresql`) |
| GET | `/api/profile` | снимок вкуса: зрелость, сигналы, топ артистов и кластеров, границы explore. Только чтение |
| PUT | `/api/profile/explore` | `{explore_lo, explore_hi}` |
| GET | `/api/contexts` | контексты `mood`, `place`, `activity`. `?all=1` — с архивными |
| POST | `/api/contexts` | `{kind, name, icon?, influence?, learning_enabled?, seeds?}` |
| GET | `/api/contexts/{id}` | один контекст |
| PATCH | `/api/contexts/{id}` | правка |
| DELETE | `/api/contexts/{id}` | в архив |
| POST | `/api/contexts/{id}/activate` | включить в сессии |
| POST | `/api/contexts/{id}/deactivate` | выключить |
| POST | `/api/session/contexts` | заменить активный набор контекстов сессии |
| GET | `/api/rules` | `block`, `downrank`, `cooldown`. `?all=1` — с архивными |
| POST | `/api/rules` | цель `track`, `song`, `artist`, `album`, `genre` или `cluster`. Можно передать `preset`, `track_id`, `artist`, `album` |
| PATCH | `/api/rules/{id}` | правка |
| DELETE | `/api/rules/{id}` | в архив |
| POST | `/api/rules/undo` | вернуть последнее архивное правило |
| POST | `/api/share/radio` | `{url, token}` — ссылка `/listen/<token>.mp3` |
| GET | `/api/share/radio` | ссылки профиля. `?all=1` — с отозванными |
| DELETE | `/api/share/radio/{token}` | отозвать |
| GET | `/api/metrics/weekly` | семь дней. Поле `listening`: `listened_sec`, `plays`, `finishes`, `finish_rate`, `top_tracks`, `top_artists`. Экран профиля читает его. Рядом в том же ответе лежат служебные срезы исходов |
| GET | `/api/metrics/listening` | `on_this_day` (тот же календарный день прошлых лет) и `year` (этот год: время, доля дослушиваний, топы) |
| GET | `/api/metrics/recommendations` | 90 дней исходов, latency p50/p95/p99, последняя политика, недавние запросы, прогоны обучения, состояние explore. На экран недели не выводится |

Лимит одновременных слушателей шары — `MUSIK_SHARE_MAX_LISTENERS` (по умолчанию 4). Абсолютный URL берётся из `MUSIK_PUBLIC_BASE_URL`.

## admin

Токен устройства сюда не входит, кроме двух исключений у заданий.

| Метод | Путь | Что делает |
|-------|------|------------|
| POST | `/api/admin/invitations` | приглашение. Секрет в ответе один раз |
| GET | `/api/admin/invitations` | список без секретов |
| GET | `/api/admin/invitations/{id}` | одно приглашение, без секрета |
| DELETE | `/api/admin/invitations/{id}` | отозвать неиспользованное |
| GET | `/api/admin/users` | учётки и статусы |
| PATCH | `/api/admin/users/{id}` | статус или роль администратора |
| POST | `/api/reload` | перечитать индекс. Воркер с loopback проходит без сессии; иначе нужен admin или Bearer. Ответ `{ok, tracks}` |
| GET | `/api/settings` | сохранённый `.env` и то, с чем процесс сейчас слушает: адрес, публичный URL, папка музыки |
| PUT | `/api/settings` | записать адрес, публичный URL и библиотеку в `.env`. Применяется после перезапуска |
| GET | `/api/settings/dirs` | подпапки `?path=` для выбора папки. Пустой path — корни дисков |
| GET | `/api/settings/external-ip` | внешний IP (кэш 10 минут). `?refresh=1` спрашивает снова |
| GET | `/api/settings/qr` | SVG. Обязателен `?url=` — публичный адрес. В код входит `musik://connect?url=…&token=…` |
| POST | `/api/settings/restart` | перезапуск воркера и плеера с записанными настройками |
| POST | `/api/library/upload` | multipart: `file` и `path` внутри библиотеки |
| POST | `/api/library/rescan` | поставить `full_rescan`. В ответе одинаковые `id` и `job_id` |
| GET | `/api/jobs` | список. `?status=` |
| GET | `/api/jobs/{id}` | одно задание. Токен устройства может прочитать своё |
| POST | `/api/jobs/{kind}` | `scan`, `embed`, `clusters`, `daily`, `album_tips`, `full_rescan`, `mix_pack`, `train_ranker`. Токен устройства может поставить только `mix_pack`. В ответе одинаковые `id` и `job_id` |
| GET | `/api/plugins` | установленные плагины и manifest, оставленные неактивными. Пути executable в ответе нет |
| POST | `/api/plugins/{id}/import` | `{url}` — задача `library.import_url`. URL хранится у задачи и не попадает в ответ, статус и логи. Успех ставит `full_rescan`; в ответе одинаковые `id` и `job_id`. Неверный URL — `400 invalid_url`, нет плагина — `404`, нет каталога библиотеки — `503 library_unavailable` |
| POST | `/api/plugins/{id}/search` | `{query, limit?}` — синхронный `library.search`. `limit` от 1 до 20, по умолчанию 8. Ответ `{items}`: `title`, `artist`, `url`, `duration`. `url` — непрозрачный адрес для импорта. Запрос и адреса не попадают в логи и argv. Обрыв клиента шлёт `SIGTERM`. Таймаут короче скачивания. Не создаёт задачу и не ставит `full_rescan`. Нет плагина или нет `library.search` — `404`, плохой запрос — `400 invalid_query`, плохой `limit` — `400 invalid_limit`, таймаут — `504` |
| GET | `/api/plugin-jobs` | список задач плагинов. `?status=` и `?limit=` |
| GET | `/api/plugin-jobs/{id}` | статус, последний progress (`pct`, `phase`, `message`, `items` по трекам: `title`, `status`, `pct`), result и `downstream_job_id` задания `full_rescan` после успешного импорта |
| POST | `/api/plugin-jobs/{id}/cancel` | отмена `pending` или `running`. Уже завершённая задача — `409 not_cancellable` |

Токен устройства к плагинам не допускается. Контракт executable — [sdk/plugin/v1/README.md](../sdk/plugin/v1/README.md).

Переменные окружения — в [CONFIG.md](CONFIG.md). Установка — в [DEPLOY.md](DEPLOY.md).
