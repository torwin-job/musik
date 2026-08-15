from __future__ import annotations

from functools import lru_cache
from pathlib import Path

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

ROOT = Path(__file__).resolve().parents[2]


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="MUSIK_", env_file=".env", extra="ignore")

    library: Path = Field(default_factory=lambda: ROOT / "data" / "music")
    data_dir: Path = Field(default_factory=lambda: ROOT / "data")
    db_path: Path = Field(default_factory=lambda: ROOT / "data" / "db" / "musik.db")
    embeddings_cache: Path = Field(
        default_factory=lambda: ROOT / "data" / "cache" / "embeddings"
    )
    artwork_cache: Path = Field(default_factory=lambda: ROOT / "data" / "cache" / "artwork")

    extensions: tuple[str, ...] = (".mp3", ".flac", ".m4a", ".wav", ".ogg", ".opus")
    analysis_seconds: float = 60.0
    workers: int = 4
    embedding_model: str = "clap"
    clap_model: str = "laion/larger_clap_music_and_speech"
    # Три окна: начало / середина / конец (сек каждое)
    embed_segment_sec: float = 30.0
    embed_workers: int = 1
    # Доля «дальних» треков в плейлистах (exploration / bandit)
    explore_ratio: float = 0.25
    worker_addr: str = "127.0.0.1:8790"
    worker_poll_sec: float = 5.0
    # Rebuild all recommendation shelves once per night. "local" uses the
    # worker host/container timezone; an IANA name (e.g. Europe/Moscow) is safer
    # for containers whose system timezone is UTC.
    nightly_mixes_enabled: bool = True
    nightly_mixes_hour: int = Field(default=3, ge=0, le=23)
    nightly_mixes_timezone: str = "local"
    # Player notifies itself after embed/rescan (set by musik-player autostart)
    player_reload_url: str = "http://127.0.0.1:8787/api/reload"
    # Same token as Go player — sent as Bearer on reload callback
    api_token: str = ""
    # Library folder watcher (musik watch)
    watch_debounce_sec: float = 45.0
    watch_clusters: bool = True
    watch_mixes: bool = True

    def ensure_dirs(self) -> None:
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        self.embeddings_cache.mkdir(parents=True, exist_ok=True)
        self.artwork_cache.mkdir(parents=True, exist_ok=True)


@lru_cache
def get_settings() -> Settings:
    s = Settings()
    s.ensure_dirs()
    return s
