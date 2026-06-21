#!/usr/bin/env python3
"""Strip AI-style comment scaffolding from Go source files."""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

SKIP_DIRS = {"gen", "bin", ".git"}

# Lines to drop entirely
DROP_LINE_PATTERNS = [
    re.compile(r"^\s*//\s*=+\s*$"),
    re.compile(r"^\s*//\s*-+\s*$"),
    re.compile(r"^\s*//\s*File:\s"),
    re.compile(r"^\s*//\s*(NEW|MODIFIED|ADD THIS|CRITICAL|UPDATED|REFACTORED|CORRECTED|THE CORE|ACCEPT the)"),
    re.compile(r"^\s*//\s*These are placeholder imports"),
    re.compile(r"^\s*//\s*You would replace"),
    re.compile(r"^\s*//\s*<--\s*CHANGE"),
    re.compile(r"^\s*//\s*Recommended for forward"),
    re.compile(r"^\s*//\s*pkg/"),
    re.compile(r"^\s*//\s*in internal/"),
]

# Inline suffixes to trim from otherwise-keep lines
INLINE_TRIM = [
    re.compile(r"\s*//\s*NEW:.*$"),
    re.compile(r"\s*//\s*MODIFIED:.*$"),
    re.compile(r"\s*//\s*ADD THIS.*$"),
    re.compile(r"\s*//\s*<--.*$"),
    re.compile(r"\s*//\s*DEPRECATED.*$"),
]


def should_process(path: Path) -> bool:
    parts = set(path.parts)
    if parts & SKIP_DIRS:
        return False
    return path.suffix == ".go"


def clean_content(text: str) -> str:
    lines = text.splitlines(keepends=True)
    out = []
    i = 0
    while i < len(lines):
        line = lines[i]

        if any(p.match(line) for p in DROP_LINE_PATTERNS):
            i += 1
            continue

        # Drop large commented-out code blocks (3+ consecutive // lines that look like code)
        if re.match(r"^\s*//\s*\w", line) and not re.match(r"^\s*//\s*[A-Z][a-z].*\.$", line):
            block = [line]
            j = i + 1
            while j < len(lines) and re.match(r"^\s*//", lines[j]):
                block.append(lines[j])
                j += 1
            if len(block) >= 5:
                code_like = sum(1 for b in block if re.search(r"//\s*(\w+\(|func |if |return |var |:=)", b))
                if code_like >= 2:
                    i = j
                    continue

        for p in INLINE_TRIM:
            line = p.sub("", line)

        out.append(line)
        i += 1

    # Collapse 3+ blank lines to 2
    result = "".join(out)
    result = re.sub(r"\n{4,}", "\n\n\n", result)
    return result


def main():
    targets = []
    if len(sys.argv) > 1:
        targets = [Path(p) for p in sys.argv[1:]]
    else:
        for p in ROOT.rglob("*.go"):
            if should_process(p):
                targets.append(p)

    changed = 0
    for path in sorted(targets):
        original = path.read_text(encoding="utf-8")
        cleaned = clean_content(original)
        if cleaned != original:
            path.write_text(cleaned, encoding="utf-8")
            changed += 1
            print(f"cleaned: {path.relative_to(ROOT)}")

    print(f"done   {changed} files updated")


if __name__ == "__main__":
    main()
