from __future__ import annotations

import json
import uuid
from datetime import datetime, timezone
from typing import Any

from services.config import DATA_DIR


LOG_TYPE_ACCOUNT = "account"


class LogService:
    def __init__(self) -> None:
        self.path = DATA_DIR / "logs.jsonl"

    def add(self, log_type: str, summary: str, detail: dict[str, Any] | None = None) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        item = {
            "id": uuid.uuid4().hex,
            "time": datetime.now(timezone.utc).isoformat(),
            "type": str(log_type or "system"),
            "summary": str(summary or ""),
            "detail": detail or {},
        }
        with self.path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(item, ensure_ascii=False, separators=(",", ":")) + "\n")


log_service = LogService()
