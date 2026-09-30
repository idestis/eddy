#!/usr/bin/env python3
"""Fail if any relative href/src in the given static site points at a missing file."""
import pathlib
import re
import sys
from urllib.parse import urlsplit, unquote

root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "landing").resolve()
base = re.compile(r"<base\b[^>]*>", re.I)
attr = re.compile(r"""(?:href|src)\s*=\s*["']([^"']+)["']""", re.I)
missing = []
for page in root.rglob("*.html"):
    for ref in attr.findall(base.sub("", page.read_text(encoding="utf-8"))):
        u = urlsplit(ref)
        if u.scheme or u.netloc or ref.startswith(("#", "mailto:", "data:")):
            continue
        path = unquote(u.path)
        if not path:
            continue
        target = (root / path.lstrip("/")) if path.startswith("/") else (page.parent / path)
        if target.is_dir():
            target = target / "index.html"
        if not target.exists():
            missing.append(f"{page.relative_to(root)}: {ref}")
if missing:
    print("Broken links:\n  " + "\n  ".join(missing))
    sys.exit(1)
print(f"ok: links in {root.name}/ resolve")
