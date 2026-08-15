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
- Candidate pool уже при N≥8000 (`MUSIK_CANDIDATE_POOL_AT`)

### Комфортно под 50k (рекомендуется)

- **6–8 vCPU**, **16 GB RAM**
- **GPU** с ≥8 GB VRAM (RTX 3060/4060 / cloud T4) для CLAP
- SSD 200+ GB если FLAC-библиотека большая
- Отдельный диск/volume под `MUSIK_LIBRARY` (RO mount в Compose)

### Запас под share + несколько устройств

- +2 CPU если часто шаришь эфир (ffmpeg LAME)
- `MUSIK_SHARE_MAX_LISTENERS=2–4`

## Env для большой библиотеки

```bash
MUSIK_WORKERS=6              # scan parallelism
MUSIK_CANDIDATE_POOL_AT=8000 # shortlist в Go queue (уже default)
# тест shortlist на малой библиотеке:
# MUSIK_CANDIDATE_POOL_AT=50
```

SimsTo в Go параллелится по `GOMAXPROCS` при N≥1500.

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

- Radio/skip: с candidate pool — миллисекунды–десятки ms на очередь
- Первый cold start UI `/api/library` — тяжёлый JSON; лучше полки artists/albums
- ANN (HNSW) — следующий шаг, если pool всё ещё маловат; сейчас не обязателен при 50k + pool
