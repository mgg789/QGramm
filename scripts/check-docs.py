#!/usr/bin/env python3
"""Check local Markdown links, UTF-8 and JSON evidence without network access."""
import json
import pathlib
import re
import sys
from urllib.parse import unquote, urlsplit

root = pathlib.Path(__file__).resolve().parents[1]
errors = []
links = 0
for path in [root/'README.md', root/'README.ru.md', *sorted((root/'docs').rglob('*.md'))]:
    try:
        content = path.read_text(encoding='utf-8')
    except UnicodeError:
        errors.append(f'{path.relative_to(root)}: invalid UTF-8')
        continue
    for match in re.finditer(r'\]\(([^)]+)\)', content):
        target = match.group(1).strip()
        if target.startswith('<'):
            target = target[1:target.index('>')]
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or not parsed.path:
            continue
        links += 1
        if not (path.parent/unquote(parsed.path)).exists():
            errors.append(f'{path.relative_to(root)}: missing {parsed.path}')
for path in sorted((root/'docs').rglob('*.json')):
    try:
        json.loads(path.read_text(encoding='utf-8'))
    except (UnicodeError, json.JSONDecodeError) as error:
        errors.append(f'{path.relative_to(root)}: invalid JSON: {error}')
if errors:
    print('\n'.join(errors), file=sys.stderr)
    sys.exit(1)
print(f'PASS: {links} local links, documentation UTF-8 and JSON evidence')
