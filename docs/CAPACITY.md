# Capacity — musik на ~50 000 треков

Оценка под self-hosted «личный Spotify» с CLAP + Go realtime.

## Где съедаются ресурсы

| Этап | CPU/GPU | RAM | Диск | Время (ориентир) |
|------|---------|-----|------|------------------|
| **scan** (tags + audio analysis) | CPU multi-thread (`MUSIK_WORKERS`) | 1–2 GB | artwork cache | ~2–8 ч на 50k (зависит от extract_audio) |
| **embed** (CLAP) | **GPU сильно желателен** | 4–8 GB (модель) + batch | embeddings cache | GPU: ~5–20 ч; **CPU: сутки+** |
| **clusters / mixes** | CPU | 1–2 GB | DB | минуты |
| **player runtime** | 1–2 CPU | **матрица N×512 float32** + Go | DB RO | постоянно |
| **share radio** (ffmpeg) | 1 CPU / слушатель | ~100–200 MB | — | пока слушают |

Матрица в RAM player: `50_000 × 512 × 4 байта ≈ 100 MB` только эмбеддинги (+ meta ~десятки MB). С запасом **player ~256–512 MB**.

## Рекомендуемый VPS / домашняя машина

### Минимум (CPU-only, терпимо ждать первый embed)

- **4 vCPU**, **8 GB RAM**, **100+ GB SSD** (музыка + cache)
- Первый `embed` на CPU — долго; дальше cache на диске
- Exact fused scan измеряется через `make bench`; shortlist больше нет,
  очередь всегда идёт через `BuildCore`

### Комфортно под 50k (рекомендуется)

- **6–8 vCPU**, **16 GB RAM**
- **GPU** с ≥8 GB VRAM (RTX 3060/4060 / cloud T4) для CLAP
- SSD 200+ GB если FLAC-библиотека большая
- Отдельный диск/volume под `MUSIK_LIBRARY`: у player монтирование на запись, у worker только чтение

### Запас под share + несколько устройств

- +2 CPU если часто шаришь эфир (ffmpeg LAME)
- `MUSIK_SHARE_MAX_LISTENERS=2–4`

## Env для большой библиотеки

```bash
MUSIK_WORKERS=6              # scan parallelism
MUSIK_QUEUE_SIZE=6
```

Для нескольких similarity-сигналов Go использует один fused exact pass по
матрице. Старый одиночный `SimsTo` остаётся для отдельных запросов и
параллелится по `GOMAXPROCS` при N≥1500.

## Измеренный exact baseline

`make bench` создаёт синтетические матрицы 2k/50k×512 и отдельно измеряет fused
scan и полную сборку очереди. На тестовой машине (Intel i7-13700F):

| Benchmark | 2k | 50k |
|-----------|----|-----|
| Fused exact scan | ~2,2 ms | ~56,5 ms |
| Exact queue build | ~2,1 ms | ~58,7 ms |

Это baseline конкретной машины, а не универсальный SLA. На целевом сервере
стоит повторить `make bench` и смотреть p95 `queue_build` в
`GET /api/metrics/recommendations`.

## Прогресс pipeline

CLI: rich progress bar (`musik scan` / `musik embed`).

Jobs / UI: `GET /api/jobs/{id}` → поле `progress`:

```json
{
  "status": "running",
  "progress": {
    "phase": "embed",
    "done": 1200,
    "total": 50000,
    "pct": 2.4,
    "message": "embed 1200/50000 (2.4%) · new=800 cache=400"
  }
}
```

Worker пишет progress в `jobs.result_json` каждые ~25 файлов (scan) / каждый трек (embed).

## Порядок первого прогона на 50k

```bash
# 1) только теги (быстро) — опционально
musik scan --tags-only   # если флаг есть; иначе полный scan

# 2) полный scan
musik scan

# 3) embed (лучше на GPU, оставить на ночь)
musik embed

# 4) clusters + mixes
musik clusters
# или POST /api/jobs/mix_pack

# 5) player
export MUSIK_PASSWORD=… MUSIK_API_TOKEN=…
./player/bin/musik-player
```

Через API: `POST /api/library/rescan` → poll `GET /api/jobs/{id}` и смотри `progress`.

## Бэкап

- `data/db/musik.db` (+ `-wal`/`-shm` при копировании останови player/worker или `sqlite3 .backup`)
- `data/cache/embeddings` — не пересчитывать CLAP заново

## Чего ждать по UX на 50k

- Radio/skip: exact queue build на 50k — около 59 ms на тестовой desktop-машине;
  живой p95 смотри в профиле или `GET /api/metrics/recommendations`
- Первый cold start UI `/api/library` — тяжёлый JSON; лучше полки artists/albums
- ANN/HNSW не вводится заранее: решение принимается только если exact scan не
  проходит утверждённый бюджет на целевой машине
