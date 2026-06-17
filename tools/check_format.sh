#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

python3 - "$ROOT" <<'PY'
import fnmatch
import subprocess
import sys
from pathlib import Path

root = Path(sys.argv[1])

binary_suffixes = {
    ".exe",
    ".logd",
    ".png",
    ".pyc",
    ".pyo",
}

space_patterns = [
    "*.rs",
    "*.py",
    "*.java",
    "*.hs",
    "*.sql",
    "*.c",
    "*.h",
    "*.cpp",
    "*.hpp",
    "*.ts",
    "*.tsx",
    "*.js",
    "*.jsx",
    "*.json",
    "*.yml",
    "*.yaml",
    "*.md",
    "*.html",
    "*.css",
    "*.rb",
    "*.lua",
    "*.pl",
    "*.sh",
    "*.tf",
]

tab_patterns = [
    "*.go",
    "Makefile",
]


def tracked_files() -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard"],
        cwd=root,
        check=True,
        capture_output=True,
        text=True,
    )
    return [root / line for line in result.stdout.splitlines() if line]


def indent_style(relpath: str) -> str | None:
    name = Path(relpath).name
    for pattern in tab_patterns:
        if fnmatch.fnmatch(name, pattern) or fnmatch.fnmatch(relpath, pattern):
            return "tab"
    for pattern in space_patterns:
        if fnmatch.fnmatch(name, pattern) or fnmatch.fnmatch(relpath, pattern):
            return "space"
    return None


def is_binary(path: Path) -> bool:
    if path.suffix.lower() in binary_suffixes:
        return True
    try:
        data = path.read_bytes()
    except OSError:
        return True
    return b"\0" in data


def line_indent(line: str) -> str:
    index = 0
    while index < len(line) and line[index] in " \t":
        index += 1
    return line[:index]


def main() -> int:
    failures: list[str] = []
    for path in tracked_files():
        relpath = str(path.relative_to(root))
        if is_binary(path):
            continue

        data = path.read_bytes()
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            failures.append(f"{relpath}: not valid UTF-8")
            continue

        if b"\r\n" in data or b"\r" in data:
            failures.append(f"{relpath}: must use LF line endings")
        if data and not data.endswith(b"\n"):
            failures.append(f"{relpath}: missing final newline")

        style = indent_style(relpath)
        for line_number, line in enumerate(text.splitlines(), start=1):
            if line.rstrip(" \t") != line:
                failures.append(f"{relpath}:{line_number}: trailing whitespace")
            indent = line_indent(line)
            if style == "space" and "\t" in indent:
                failures.append(f"{relpath}:{line_number}: leading tabs are not allowed")
            if style == "tab" and indent.startswith(" "):
                failures.append(f"{relpath}:{line_number}: leading spaces are not allowed")

    if failures:
        print("Formatting violations:")
        for failure in failures:
            print(f"  - {failure}")
        return 1

    print("Formatting check passed.")
    return 0


raise SystemExit(main())
PY
