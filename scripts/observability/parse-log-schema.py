#!/usr/bin/env python3
"""Parse the log-schema / metric-catalog markdown docs into structured JSON.

Ticket #397. The doc is the contract. The CI gate (deferred to a follow-up)
will consume the structured representation this script emits, compare it
against the live log/metric output, and fail the build on drift.

Two usages today:

1. As a smoke test (see scripts/observability/test-doc-parses.sh):
   verifies the doc is well-formed by parsing it without crashing and
   emitting a non-empty matrix.

2. As an input to the future CI gate. The follow-up ticket will read this
   script's JSON output directly.

Usage:

    parse-log-schema.py docs/observability/log-schema.md
    parse-log-schema.py docs/observability/metric-catalog.md

Both modes share the same table extractor; we identify which doc we're
parsing by the title heading.

Also runs in --validate-examples mode against log-schema.md, which extracts
the ```json fenced code blocks and asserts each parses as valid JSON.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass, asdict
from pathlib import Path


# ---------------------------------------------------------------------------
# Markdown table extraction.
#
# The matrix tables we care about have a known header shape. We identify
# them by header row and extract every row below them until the blank line.
# ---------------------------------------------------------------------------


LOG_HEADER_TOKENS = ("Field", "Type", "Tier", "Since", "Notes")
METRIC_HEADER_TOKENS = ("Name", "Type", "Labels", "Tier", "Since")


@dataclass
class LogField:
    name: str
    type: str
    tier: str
    since: str
    notes: str


@dataclass
class MetricRow:
    name: str
    type: str
    labels: str
    tier: str
    since: str
    cardinality: str


def _split_row(line: str) -> list[str]:
    """Split a markdown table row into trimmed cell contents.

    Strips backtick wrappers so `field_name` becomes field_name.
    """
    # A row looks like '| a | b | c |'. Trim leading/trailing pipes, split.
    inner = line.strip().strip("|")
    cells = [c.strip() for c in inner.split("|")]
    # Strip surrounding backticks from a "monospaced" cell, but only at the
    # outer boundary. Inner code spans inside a notes column are preserved.
    out = []
    for c in cells:
        if c.startswith("`") and c.endswith("`") and c.count("`") == 2:
            out.append(c[1:-1])
        else:
            out.append(c)
    return out


def _is_separator(line: str) -> bool:
    """The '|---|---|---|' row directly under a header."""
    return bool(re.match(r"^\|[\s\-:|]+\|\s*$", line.strip()))


def _is_header(cells: list[str], tokens: tuple[str, ...]) -> bool:
    if len(cells) < len(tokens):
        return False
    return all(t.lower() in cells[i].lower() for i, t in enumerate(tokens))


def parse_log_schema(text: str) -> list[LogField]:
    """Extract the field-stability matrix from log-schema.md."""
    rows: list[LogField] = []
    in_table = False
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        line = lines[i]
        cells = _split_row(line) if line.startswith("|") else []
        if not in_table:
            if _is_header(cells, LOG_HEADER_TOKENS):
                # Next line is the separator; the row after that is data.
                if i + 1 < len(lines) and _is_separator(lines[i + 1]):
                    in_table = True
                    i += 2
                    continue
        else:
            if not line.startswith("|"):
                in_table = False
                i += 1
                continue
            # Data row.
            if len(cells) >= 5:
                rows.append(
                    LogField(
                        name=cells[0],
                        type=cells[1],
                        tier=cells[2],
                        since=cells[3],
                        notes=cells[4],
                    )
                )
        i += 1
    return rows


def parse_metric_catalog(text: str) -> list[MetricRow]:
    """Extract metric tables from metric-catalog.md.

    The catalog has multiple metric tables (interface engine, HAPI, etc.);
    we concatenate them all.
    """
    rows: list[MetricRow] = []
    in_table = False
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        line = lines[i]
        cells = _split_row(line) if line.startswith("|") else []
        if not in_table:
            if _is_header(cells, METRIC_HEADER_TOKENS):
                if i + 1 < len(lines) and _is_separator(lines[i + 1]):
                    in_table = True
                    i += 2
                    continue
        else:
            if not line.startswith("|"):
                in_table = False
                i += 1
                continue
            if len(cells) >= 6:
                rows.append(
                    MetricRow(
                        name=cells[0],
                        type=cells[1],
                        labels=cells[2],
                        tier=cells[3],
                        since=cells[4],
                        cardinality=cells[5],
                    )
                )
        i += 1
    return rows


# ---------------------------------------------------------------------------
# Worked-example JSON validation. Every ```json fenced block in the schema
# doc has to itself be valid JSON, otherwise the doc misleads agents.
# ---------------------------------------------------------------------------


_FENCE_RE = re.compile(r"```json\s*\n(.*?)```", re.DOTALL)


def extract_json_examples(text: str) -> list[str]:
    """Return every ```json fenced block in the doc."""
    return _FENCE_RE.findall(text)


def validate_examples(text: str) -> list[tuple[int, str]]:
    """Try to json.loads every fenced block. Return (index, error) for failures."""
    failures: list[tuple[int, str]] = []
    for idx, block in enumerate(extract_json_examples(text)):
        # The schema doc has one ```bash example as well (jq invocation);
        # we only fenced-tagged as ```json the actual records.
        try:
            json.loads(block)
        except json.JSONDecodeError as exc:
            failures.append((idx, str(exc)))
    return failures


# ---------------------------------------------------------------------------
# Entry point.
# ---------------------------------------------------------------------------


def main(argv: list[str]) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("path", type=Path, help="Path to the markdown doc")
    p.add_argument(
        "--validate-examples",
        action="store_true",
        help="Also assert every ```json block is valid JSON (log-schema only).",
    )
    args = p.parse_args(argv)

    if not args.path.exists():
        print(f"error: file not found: {args.path}", file=sys.stderr)
        return 2

    text = args.path.read_text(encoding="utf-8")

    out: dict[str, object] = {"source": str(args.path)}

    # Pick the parser by content. log-schema has the LogField header tokens;
    # metric-catalog has the MetricRow header tokens.
    if "field-stability matrix" in text.lower() or "field            | type" in text.lower().replace("`", ""):
        fields = parse_log_schema(text)
        out["kind"] = "log-schema"
        out["fields"] = [asdict(f) for f in fields]
        out["count"] = len(fields)
        if args.validate_examples:
            failures = validate_examples(text)
            out["example_failures"] = [
                {"index": i, "error": e} for i, e in failures
            ]
        if not fields:
            print("error: no log fields extracted from doc", file=sys.stderr)
            return 1
    elif "metric catalog" in text.lower():
        metrics = parse_metric_catalog(text)
        out["kind"] = "metric-catalog"
        out["metrics"] = [asdict(m) for m in metrics]
        out["count"] = len(metrics)
        if not metrics:
            print("error: no metrics extracted from doc", file=sys.stderr)
            return 1
    else:
        print(
            "error: could not identify doc type from content (expected log-schema "
            "or metric-catalog)",
            file=sys.stderr,
        )
        return 2

    if args.validate_examples and out.get("example_failures"):
        # Non-empty failure list -> non-zero exit. Doc is broken.
        print(json.dumps(out, indent=2))
        return 1

    json.dump(out, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
