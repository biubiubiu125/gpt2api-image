from __future__ import annotations

import os
from pathlib import Path


BASE_DIR = Path(__file__).resolve().parents[1]
DATA_DIR = Path(os.getenv("GPT2API_IMAGE_DATA_DIR") or BASE_DIR / "data")
DATA_DIR.mkdir(parents=True, exist_ok=True)


class RuntimeConfig:
    @property
    def go_api_base_url(self) -> str:
        return (
            os.getenv("GPT2API_IMAGE_API_BASE_URL")
            or os.getenv("GPT2API_IMAGE_GO_API_URL")
            or "http://api"
        ).strip().rstrip("/")

    @property
    def register_internal_key(self) -> str:
        return (os.getenv("GPT2API_IMAGE_REGISTER_INTERNAL_KEY") or "").strip()

    @property
    def auth_key(self) -> str:
        return (os.getenv("GPT2API_IMAGE_AUTH_KEY") or "").strip()


config = RuntimeConfig()
