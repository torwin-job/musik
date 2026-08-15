#!/usr/bin/env python3
"""Export full paper-ready cosine calculation for two fixed tracks."""

from __future__ import annotations

from pathlib import Path

import numpy as np

from musik.config import get_settings
from musik.db import get_embedding
from musik.db.schema import connect
from musik.embed.segments import DEFAULT_SEGMENT_SEC, DEFAULT_SR, SEGMENT_STRATEGY, plan_windows

ROOT = Path(__file__).resolve().parents[1]
DOCS = ROOT / "docs"
AID, BID = 102, 103


def main() -> None:
    with connect() as c:
        a_row = dict(c.execute("SELECT * FROM tracks WHERE id=?", (AID,)).fetchone())
        b_row = dict(c.execute("SELECT * FROM tracks WHERE id=?", (BID,)).fetchone())
        fa = dict(
            c.execute(
                "SELECT track_id, embedding_dim, status, computed_at, length(embedding) AS blob_len "
                "FROM features WHERE track_id=?",
                (AID,),
            ).fetchone()
        )
        fb = dict(
            c.execute(
                "SELECT track_id, embedding_dim, status, computed_at, length(embedding) AS blob_len "
                "FROM features WHERE track_id=?",
                (BID,),
            ).fetchone()
        )
        model_row = c.execute("SELECT value FROM scan_state WHERE key='clap_model'").fetchone()
        model = model_row["value"] if model_row else get_settings().clap_model

    a = get_embedding(AID).astype(np.float64)
    b = get_embedding(BID).astype(np.float64)
    prod = a * b
    a2 = a * a
    b2 = b * b

    Ta = float(a_row["duration"])
    Tb = float(b_row["duration"])
    wa = plan_windows(Ta, DEFAULT_SEGMENT_SEC)
    wb = plan_windows(Tb, DEFAULT_SEGMENT_SEC)

    na = float(np.linalg.norm(a))
    nb = float(np.linalg.norm(b))
    dot = float(np.dot(a, b))
    cos = dot / (na * nb)
    dist = float(np.linalg.norm(a - b))
    theta = float(np.degrees(np.arccos(np.clip(cos, -1.0, 1.0))))
    dist_from_cos = float(np.sqrt(2.0 - 2.0 * cos))

    DOCS.mkdir(parents=True, exist_ok=True)
    csv_path = DOCS / "paper-cosine-full-table.csv"
    with csv_path.open("w", encoding="utf-8") as f:
        f.write("i,a_i_Nevidimki,b_i_Drugie,product_a_i_times_b_i,a_i_squared,b_i_squared\n")
        for i in range(512):
            f.write(
                f"{i},{a[i]:.10f},{b[i]:.10f},{prod[i]:.10f},{a2[i]:.10f},{b2[i]:.10f}\n"
            )

    blocks = []
    for k in range(0, 512, 64):
        blocks.append(
            (
                k,
                k + 63,
                float(prod[k : k + 64].sum()),
                float(a2[k : k + 64].sum()),
                float(b2[k : k + 64].sum()),
            )
        )

    def dump_vec(name: str, v: np.ndarray) -> str:
        lines = [f"### {name}\n", "```"]
        for i in range(0, 512, 8):
            chunk = ", ".join(f"{v[j]: .8f}" for j in range(i, min(i + 8, 512)))
            lines.append(f"[{i:3d}..{min(i + 7, 511):3d}] {chunk}")
        lines.append("```\n")
        return "\n".join(lines)

    lines: list[str] = []
    lines += [
        "# Полный расчёт cosine: «Невидимки» ↔ «Другие»",
        "",
        "Здесь собраны **все формулы**, **откуда взялось каждое значение**, "
        "и **все 512 координат** обоих векторов.",
        "",
        "Рядом лежит машиночитаемая таблица: "
        "[`paper-cosine-full-table.csv`](paper-cosine-full-table.csv).",
        "",
        "---",
        "",
        "## 0. Что считаем",
        "",
        "| | Трек A | Трек B |",
        "|---|---|---|",
        f"| Название | {a_row['artist']} — {a_row['title']} | {b_row['artist']} — {b_row['title']} |",
        f"| id в SQLite `tracks` | {a_row['id']} | {b_row['id']} |",
        f"| path | `{a_row['path']}` | `{b_row['path']}` |",
        f"| file_md5 | `{a_row['file_md5']}` | `{b_row['file_md5']}` |",
        f"| duration, сек | {a_row['duration']} | {b_row['duration']} |",
        f"| bitrate | {a_row['bitrate']} | {b_row['bitrate']} |",
        f"| features.status | {fa['status']} | {fb['status']} |",
        f"| embedding_dim | {fa['embedding_dim']} | {fb['embedding_dim']} |",
        f"| blob bytes (512×float32) | {fa['blob_len']} | {fb['blob_len']} |",
        f"| computed_at | {fa['computed_at']} | {fb['computed_at']} |",
        "",
        "**Итог, который воспроизводим:**",
        "",
        "$$",
        f"\\cos(a,b) = {cos:.15f} \\approx 0.895",
        "$$",
        "",
        "---",
        "",
        "## 1. Откуда взялись векторы a и b",
        "",
        "### 1.1. Константы системы",
        "",
        "| Константа | Значение | Откуда |",
        "|---|---|---|",
        f"| Модель CLAP | `{model}` | `settings.clap_model` / HuggingFace |",
        f"| Стратегия окон | `{SEGMENT_STRATEGY}` | `musik/embed/segments.py` |",
        f"| Длина окна S | {DEFAULT_SEGMENT_SEC} с | `DEFAULT_SEGMENT_SEC` |",
        f"| Sample rate | {DEFAULT_SR} Hz | требование LAION CLAP |",
        "| Размерность | 512 | выход `audio_projection` |",
        "| Норма | L2 = 1 | normalize в CLAP + после усреднения окон |",
        "",
        "### 1.2. Нарезка окон — формулы",
        "",
        "Для длительности трека \(T\) и окна \(S = 30\):",
        "",
        "$$",
        r"\begin{aligned}",
        r"\mathrm{start} &= [0,\ S) \\",
        r"\mathrm{middle\ offset} &= \max(0,\ T/2 - S/2) \\",
        r"\mathrm{end\ offset} &= \max(0,\ T - S)",
        r"\end{aligned}",
        "$$",
        "",
        f"**Трек A:** \(T_A = {Ta:.10f}\)",
        "",
    ]
    for w in wa:
        n_samples = int(round(w.duration_sec * DEFAULT_SR))
        lines.append(
            f"- `{w.name}`: offset = **{w.offset_sec:.10f}** с, "
            f"duration = **{w.duration_sec:.10f}** с, сэмплов ≈ **{n_samples}**"
        )
    lines += [
        "",
        "Подстановка:",
        "",
        f"- middle_A = {Ta:.10f}/2 − 15 = **{Ta / 2 - 15:.10f}**",
        f"- end_A = {Ta:.10f} − 30 = **{Ta - 30:.10f}**",
        "",
        f"**Трек B:** \(T_B = {Tb:.10f}\)",
        "",
    ]
    for w in wb:
        lines.append(
            f"- `{w.name}`: offset = **{w.offset_sec:.10f}** с, "
            f"duration = **{w.duration_sec:.10f}** с"
        )
    lines += [
        "",
        "Подстановка:",
        "",
        f"- middle_B = {Tb:.10f}/2 − 15 = **{Tb / 2 - 15:.10f}**",
        f"- end_B = {Tb:.10f} − 30 = **{Tb - 30:.10f}**",
        "",
        "### 1.3. Аудио окна",
        "",
        "`librosa.load(path, sr=48000, mono=True, offset=..., duration=30)`",
        "",
        "→ волна \(y \\in \\mathbb{R}^{N}\), \(N \\approx 1\\,440\\,000\), float32.",
        "",
        "### 1.4. CLAP: волна → 512 чисел",
        "",
        "Для каждого окна \(y\):",
        "",
        "1. `ClapProcessor` → мел-спектрограмма `input_features`",
        "2. audio encoder → скрытый вектор (1024 до проекции)",
        "3. `audio_projection` → \(e \\in \\mathbb{R}^{512}\)",
        "4. L2-нормализация внутри `get_audio_features`:",
        "",
        "$$",
        r"e \leftarrow \frac{e}{\|e\|_2}",
        "$$",
        "",
        "Веса сети — предобученный файл модели на HuggingFace "
        f"(`{model}`). Их не расписывают на бумаге: берут готовый выход \(e\).",
        "",
        "### 1.5. Три окна → один вектор трека",
        "",
        "Пусть \(e^{(1)}, e^{(2)}, e^{(3)}\) — выходы CLAP для start / middle / end.",
        "",
        "$$",
        r"\bar{e} = \frac{e^{(1)} + e^{(2)} + e^{(3)}}{3},"
        r"\qquad v = \frac{\bar{e}}{\|\bar{e}\|_2}",
        "$$",
        "",
        f"- Для A результат \(v\) = вектор **a** (track_id={AID})",
        f"- Для B результат \(v\) = вектор **b** (track_id={BID})",
        "- Хранение: `features.embedding` (2048 байт) + npy-кеш по MD5",
        "",
        "---",
        "",
        "## 2. Формулы похожести",
        "",
        "### 2.1. Скалярное произведение",
        "",
        "$$",
        r"a \cdot b = \sum_{i=0}^{511} a_i b_i",
        "$$",
        "",
        "### 2.2. L2-норма",
        "",
        "$$",
        r"\|a\|_2 = \sqrt{\sum_{i=0}^{511} a_i^2},"
        r"\qquad \|b\|_2 = \sqrt{\sum_{i=0}^{511} b_i^2}",
        "$$",
        "",
        "### 2.3. Cosine similarity",
        "",
        "$$",
        r"\cos(a,b) = \frac{a\cdot b}{\|a\|_2\,\|b\|_2}",
        "$$",
        "",
        "### 2.4. Упрощение (наши векторы уже единичные)",
        "",
        "$$",
        r"\boxed{\cos(a,b) = a\cdot b = \sum_{i=0}^{511} a_i b_i}",
        "$$",
        "",
        "### 2.5. Угол и евклидово расстояние",
        "",
        "$$",
        r"\theta = \arccos(\cos(a,b)),"
        r"\qquad \|a-b\|_2 = \sqrt{2-2\cos(a,b)}",
        "$$",
        "",
        "---",
        "",
        "## 3. Подстановка фактических чисел",
        "",
        "### 3.1. Нормы",
        "",
        f"- \(\\sum a_i^2\) = **{a2.sum():.15f}** → \(\\|a\\|_2\) = **{na:.15f}**",
        f"- \(\\sum b_i^2\) = **{b2.sum():.15f}** → \(\\|b\\|_2\) = **{nb:.15f}**",
        "",
        "### 3.2. Cosine",
        "",
        f"- \(a\\cdot b\) = **{dot:.15f}**",
        f"- \(\\cos(a,b)\) = **{cos:.15f}**",
        f"- \(\\theta\) = **{theta:.10f}°**",
        f"- \(\\|a-b\\|_2\) = **{dist:.15f}**",
        f"- проверка \(\\sqrt{{2-2\\cos}}\) = **{dist_from_cos:.15f}**",
        "",
        "### 3.3. Блоки по 64 (удобно складывать на бумаге)",
        "",
        "| Блок i | sum(a_i b_i) | sum(a_i²) | sum(b_i²) |",
        "|---|---:|---:|---:|",
    ]
    for lo, hi, sp, sa, sb in blocks:
        lines.append(f"| {lo}…{hi} | {sp:.15f} | {sa:.15f} | {sb:.15f} |")
    lines += [
        f"| **всего** | **{dot:.15f}** | **{a2.sum():.15f}** | **{b2.sum():.15f}** |",
        "",
        "Пошаговое сложение блоков произведений:",
        "",
        "```",
    ]
    running = 0.0
    for lo, hi, sp, sa, sb in blocks:
        running += sp
        lines.append(f"+ {sp:.15f}   (i={lo}..{hi})   → running = {running:.15f}")
    lines += [
        f"= {running:.15f}",
        "```",
        "",
        "Округление до 3 знаков: **0.895**.",
        "",
        "---",
        "",
        "## 4. Пример: первые 20 координат вручную",
        "",
        "| i | a_i (Невидимки) | b_i (Другие) | a_i·b_i | откуда |",
        "|---:|---:|---:|---:|---|",
    ]
    for i in range(20):
        lines.append(
            f"| {i} | {a[i]:.10f} | {b[i]:.10f} | {prod[i]:.10f} | "
            f"embedding id {AID} и {BID} |"
        )
    lines += [
        "",
        f"Сумма первых 20 произведений = **{prod[:20].sum():.15f}** "
        f"(часть полного {dot:.15f}).",
        "",
        "Пример одной строки:",
        "",
        "$$",
        f"a_0 \\cdot b_0 = ({a[0]:.10f})\\times({b[0]:.10f}) = {prod[0]:.10f}",
        "$$",
        "",
        "---",
        "",
        "## 5. Полные данные: все 512 значений",
        "",
        "Машинная таблица со всеми колонками:",
        "",
        f"- файл: `{csv_path.relative_to(ROOT)}`",
        "- колонки: `i`, `a_i_Nevidimki`, `b_i_Drugie`, `product_a_i_times_b_i`, "
        "`a_i_squared`, `b_i_squared`",
        "",
        "Ниже тот же дамп текстом.",
        "",
    ]
    lines.append(dump_vec("5.1. Вектор a — Невидимки (все 512)", a))
    lines.append(dump_vec("5.2. Вектор b — Другие (все 512)", b))
    lines.append(dump_vec("5.3. Произведения a_i·b_i (все 512)", prod))

    lines += [
        "---",
        "",
        "## 6. Цепочка «откуда число»",
        "",
        "| Число / объект | Откуда взялось |",
        "|---|---|",
        "| mp3 на диске | `/home/void/test/...` |",
        "| id 102 / 103 | `musik scan` → таблица `tracks` |",
        "| duration | теги/декодер при scan |",
        "| MD5 | хеш байтов файла |",
        "| окна 30+30+30 | формулы §1.2 из duration |",
        "| волны y | librosa, 48 kHz mono |",
        "| e_start/mid/end | нейросеть CLAP (веса HuggingFace) |",
        "| a, b ∈ R^512 | среднее 3 окон + L2-normalize → `features.embedding` |",
        "| каждый a_i, b_i | i-я float32-координата blob/npy |",
        "| a_i·b_i | умножение двух float |",
        f"| {cos:.6f}… | сумма всех 512 произведений |",
        "| 0.895 в UI | округление до 3 знаков |",
        "",
        "---",
        "",
        "## 7. Одна строка",
        "",
        "$$",
        f"\\cos(\\text{{Невидимки}},\\text{{Другие}})"
        f" = \\sum_{{i=0}}^{{511}} a_i b_i = {dot:.15f}",
        "$$",
        "",
        f"где \(a\) = embedding track_id={AID}, \(b\) = embedding track_id={BID}, "
        "оба уже L2-нормированы.",
        "",
    ]

    out = DOCS / "paper-cosine-nevidimki-louna.md"
    out.write_text("\n".join(lines), encoding="utf-8")
    print(f"wrote {out} ({out.stat().st_size} bytes)")
    print(f"wrote {csv_path} ({csv_path.stat().st_size} bytes)")
    print(f"cos={cos:.15f}")


if __name__ == "__main__":
    main()
