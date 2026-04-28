#!/usr/bin/env python3
"""
Backfill ai_models modality and token-limit metadata.

Spec:
- Read DB connection from repo-root .env.
- Prefer official OpenAI metadata for Wangsu-proxied OpenAI models.
- For other models, only infer input/output modalities from model_type and existing
  supports_vision feature flags; do not invent context windows.
- Dry-run by default; pass --apply to write changes.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any

import pymysql


REPO_ROOT = Path(__file__).resolve().parents[2]


OFFICIAL_MODEL_OVERRIDES: dict[str, dict[str, Any]] = {
    # OpenAI official model pages, checked 2026-04-25.
    "gpt-4": {
        "input_modalities": ["text"],
        "output_modalities": ["text"],
        "context_window": 8192,
        "max_input_tokens": 8192,
        "max_output_tokens": 8192,
    },
    "gpt-4.1": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 1047576,
        "max_input_tokens": 1047576,
        "max_output_tokens": 32768,
    },
    "gpt-4.1-mini": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 1047576,
        "max_input_tokens": 1047576,
        "max_output_tokens": 32768,
    },
    "gpt-4o-mini": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 128000,
        "max_input_tokens": 128000,
        "max_output_tokens": 16384,
    },
    "gpt-5": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 400000,
        "max_input_tokens": 400000,
        "max_output_tokens": 128000,
    },
    "gpt-5.1-chat-latest": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 128000,
        "max_input_tokens": 128000,
        "max_output_tokens": 16384,
    },
    "gpt-5.2": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 400000,
        "max_input_tokens": 400000,
        "max_output_tokens": 128000,
    },
    "gpt-5.3-codex": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 400000,
        "max_input_tokens": 400000,
        "max_output_tokens": 128000,
    },
    "gpt-5.4": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 1050000,
        "max_input_tokens": 1050000,
        "max_output_tokens": 128000,
    },
    "gpt-5.4-mini": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "context_window": 400000,
        "max_input_tokens": 400000,
        "max_output_tokens": 128000,
    },
    "gpt-image-1.5": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["text", "image"],
    },
    "gpt-image-2": {
        "input_modalities": ["text", "image"],
        "output_modalities": ["image"],
    },
    "dall-e-3": {
        "input_modalities": ["text"],
        "output_modalities": ["image"],
    },
}


def load_env() -> dict[str, str]:
    env_path = REPO_ROOT / ".env"
    env: dict[str, str] = {}
    for line in env_path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        env[key] = value
    return env


def connect():
    env = load_env()
    return pymysql.connect(
        host=env["DATABASE_HOST"],
        port=int(env.get("DATABASE_PORT", "3306")),
        user=env["DATABASE_USER"],
        password=env["DATABASE_PASSWORD"],
        database=env["DATABASE_DBNAME"],
        charset="utf8mb4",
        cursorclass=pymysql.cursors.DictCursor,
        autocommit=False,
        connect_timeout=10,
    )


def parse_json(value: Any) -> Any:
    if value in (None, "", b""):
        return None
    if isinstance(value, (dict, list)):
        return value
    if isinstance(value, bytes):
        value = value.decode("utf-8")
    try:
        return json.loads(value)
    except Exception:
        return None


def json_empty(value: Any) -> bool:
    parsed = parse_json(value)
    return parsed in (None, [], {})


def has_feature(row: dict[str, Any], key: str) -> bool:
    features = parse_json(row.get("features")) or {}
    return bool(features.get(key))


def inferred_modalities(row: dict[str, Any]) -> tuple[list[str] | None, list[str] | None]:
    model_type = (row.get("model_type") or "").strip().lower()
    name = (row.get("model_name") or "").lower()
    supports_vision = has_feature(row, "supports_vision")

    if model_type in {"llm", "reasoning", "chat", "router"}:
        return (["text", "image"] if supports_vision else ["text"]), ["text"]
    if model_type in {"vlm", "vision"}:
        return ["text", "image"], ["text"]
    if model_type == "embedding":
        inputs = ["text", "image"] if "vision" in name else ["text"]
        return inputs, ["embedding"]
    if model_type == "imagegeneration":
        inputs = ["text", "image"] if any(x in name for x in ("edit", "image-2", "image-1")) else ["text"]
        return inputs, ["image"]
    if model_type == "videogeneration":
        return ["text", "image"], ["video"]
    if model_type == "tts":
        return ["text"], ["audio"]
    if model_type == "asr":
        return ["audio"], ["text"]
    if model_type == "rerank":
        return ["text"], ["text"]
    if model_type == "3dgeneration":
        return ["text", "image"], ["3d"]
    return None, None


def planned_updates(row: dict[str, Any]) -> dict[str, Any]:
    updates: dict[str, Any] = {}
    model_name = row["model_name"]
    supplier_code = row["supplier_code"]

    if supplier_code == "wangsu_aigw" and model_name in OFFICIAL_MODEL_OVERRIDES:
        for key, value in OFFICIAL_MODEL_OVERRIDES[model_name].items():
            current = parse_json(row.get(key)) if key.endswith("_modalities") else row.get(key)
            if current != value:
                updates[key] = value
        return updates

    inferred_input, inferred_output = inferred_modalities(row)
    if inferred_input and json_empty(row.get("input_modalities")):
        updates["input_modalities"] = inferred_input
    if inferred_output and json_empty(row.get("output_modalities")):
        updates["output_modalities"] = inferred_output

    context_window = int(row.get("context_window") or 0)
    max_input_tokens = int(row.get("max_input_tokens") or 0)
    # Fill max_input only when the context was already non-default or came from
    # provider sync; leave the 4096 default alone to avoid false precision.
    if max_input_tokens == 0 and context_window not in (0, 4096):
        updates["max_input_tokens"] = context_window

    return updates


def fetch_rows(conn) -> list[dict[str, Any]]:
    with conn.cursor() as cur:
        cur.execute(
            """
            SELECT m.id, s.code AS supplier_code, m.model_name, m.display_name,
                   m.model_type, m.input_modalities, m.output_modalities,
                   m.context_window, m.max_input_tokens, m.max_output_tokens,
                   m.features, m.pricing_unit
            FROM ai_models m
            JOIN suppliers s ON s.id = m.supplier_id
            ORDER BY m.id
            """
        )
        return list(cur.fetchall())


def summarize(conn, title: str) -> None:
    with conn.cursor() as cur:
        cur.execute(
            """
            SELECT COUNT(*) total,
              SUM(input_modalities IS NULL OR JSON_LENGTH(input_modalities)=0) missing_input_modalities,
              SUM(output_modalities IS NULL OR JSON_LENGTH(output_modalities)=0) missing_output_modalities,
              SUM(context_window IS NULL OR context_window=0 OR context_window=4096) weak_context_window,
              SUM(max_input_tokens IS NULL OR max_input_tokens=0) missing_max_input,
              SUM(max_output_tokens IS NULL OR max_output_tokens=0) missing_max_output
            FROM ai_models
            """
        )
        print(title, cur.fetchone())


def run(apply: bool) -> None:
    conn = connect()
    try:
        summarize(conn, "BEFORE")
        rows = fetch_rows(conn)
        plans = [(row, planned_updates(row)) for row in rows]
        plans = [(row, updates) for row, updates in plans if updates]

        print(f"planned_rows={len(plans)} planned_field_updates={sum(len(u) for _, u in plans)}")
        for row, updates in plans[:40]:
            print(
                json.dumps(
                    {
                        "id": row["id"],
                        "supplier": row["supplier_code"],
                        "model": row["model_name"],
                        "updates": updates,
                    },
                    ensure_ascii=False,
                )
            )
        if len(plans) > 40:
            print(f"... {len(plans) - 40} more rows")

        if not apply:
            conn.rollback()
            print("DRY RUN ONLY. Re-run with --apply to write changes.")
            return

        with conn.cursor() as cur:
            for row, updates in plans:
                sets = []
                values: list[Any] = []
                for key, value in updates.items():
                    sets.append(f"{key}=%s")
                    if key.endswith("_modalities"):
                        values.append(json.dumps(value, ensure_ascii=False))
                    else:
                        values.append(value)
                values.append(row["id"])
                cur.execute(f"UPDATE ai_models SET {', '.join(sets)}, updated_at=NOW() WHERE id=%s", values)
        conn.commit()
        summarize(conn, "AFTER")
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


def self_test() -> None:
    gpt = {
        "supplier_code": "wangsu_aigw",
        "model_name": "gpt-4o-mini",
        "input_modalities": None,
        "output_modalities": None,
        "context_window": 128000,
        "max_input_tokens": 0,
        "max_output_tokens": 16384,
        "features": '{"supports_vision": true}',
        "model_type": "LLM",
    }
    assert planned_updates(gpt)["input_modalities"] == ["text", "image"]
    assert planned_updates(gpt)["output_modalities"] == ["text"]
    assert planned_updates(gpt)["max_input_tokens"] == 128000

    vlm = {"supplier_code": "x", "model_name": "qwen-vl", "model_type": "VLM", "features": "{}", "input_modalities": None, "output_modalities": None, "context_window": 4096, "max_input_tokens": 0}
    assert planned_updates(vlm)["input_modalities"] == ["text", "image"]

    llm = {"supplier_code": "x", "model_name": "plain", "model_type": "LLM", "features": "{}", "input_modalities": None, "output_modalities": None, "context_window": 4096, "max_input_tokens": 0}
    assert planned_updates(llm) == {"input_modalities": ["text"], "output_modalities": ["text"]}
    print("self-test PASS")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true", help="write updates to database")
    parser.add_argument("--self-test", action="store_true", help="run pure rule tests")
    args = parser.parse_args()

    if args.self_test:
        self_test()
        return
    run(apply=args.apply)


if __name__ == "__main__":
    main()
