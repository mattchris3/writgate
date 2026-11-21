"""Write the API's OpenAPI spec to openapi.json (or check it is current).

uv run python scripts/export_openapi.py          # write
uv run python scripts/export_openapi.py --check  # fail if out of date
"""

import json
import sys
from pathlib import Path

from payments_api import create_app

OUT = Path(__file__).resolve().parent.parent / "openapi.json"


def render() -> str:
    spec = create_app(api_key="unused").openapi()
    return json.dumps(spec, indent=2, sort_keys=False) + "\n"


def main() -> int:
    text = render()
    if "--check" in sys.argv[1:]:
        if not OUT.exists() or OUT.read_text() != text:
            print(f"{OUT.name} is out of date: run scripts/export_openapi.py", file=sys.stderr)
            return 1
        return 0
    OUT.write_text(text)
    print(f"wrote {OUT}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
