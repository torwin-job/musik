"""Human-readable explanations for playlist slots."""

from __future__ import annotations


def explain_daily(
    *,
    cosine: float,
    artist: str,
    cluster: int | None,
    diversify: bool,
    explore: bool = False,
) -> str:
    if explore:
        parts = [f"exploration · дальний трек (cosine {cosine:.3f} к вкусу)"]
    else:
        parts = [f"cosine {cosine:.3f} к центру Daily Mix"]
    if cluster is not None:
        parts.append(f"кластер {cluster}")
    if diversify and not explore:
        parts.append("MMR diversity")
    if artist:
        parts.append(f"артист: {artist}")
    return "; ".join(parts)


def explain_radio(
    *, cosine: float, seed_title: str, hop: int, explore: bool = False
) -> str:
    if hop == 0:
        return f"сид радио: «{seed_title}»"
    if explore:
        return f"шаг {hop}: exploration jump (cosine {cosine:.3f} к «{seed_title}»)"
    return f"шаг {hop}: cosine {cosine:.3f} к предыдущему («{seed_title}»)"


def explain_weekly(*, cosine: float, cluster: int | None, bucket: str) -> str:
    parts = [f"weekly · корзина «{bucket}»", f"cosine {cosine:.3f}"]
    if cluster is not None:
        parts.append(f"кластер {cluster}")
    return "; ".join(parts)


def explain_mood(*, cosine: float, mood: str, lufs: float | None, bpm: float | None) -> str:
    parts = [f"mood={mood}", f"cosine {cosine:.3f} к центру настроения"]
    if lufs is not None:
        parts.append(f"LUFS {lufs:.1f}")
    if bpm is not None:
        parts.append(f"BPM {bpm:.0f}")
    return "; ".join(parts)
