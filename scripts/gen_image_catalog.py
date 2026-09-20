#!/usr/bin/env python3
"""Convert upstream pi's generated image-model data into ai/image_catalog.json.

Upstream ground truth: pi/packages/ai/src/image-models.generated.ts, produced by
upstream's own generator (`npm run generate-image-models` in packages/ai) from
the OpenRouter models API. The generated TypeScript literal is evaluated with
Node's built-in type stripping, so the port consumes upstream's data verbatim.

Usage:
    python3 scripts/gen_image_catalog.py /path/to/pi > ai/image_catalog.json
"""
import json
import subprocess
import sys
from pathlib import Path

SCRIPT = """
const target = process.argv[process.argv.length - 1];
import(target).then((module) => {
	process.stdout.write(JSON.stringify({ providers: module.IMAGE_MODELS }));
}).catch((error) => {
	process.stderr.write(String(error && error.message ? error.message : error));
	process.exit(1);
});
"""


def main() -> None:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    path = Path(sys.argv[1]) / "packages" / "ai" / "src" / "image-models.generated.ts"
    if not path.is_file():
        print(f"error: {path} not found — run upstream's generate-image-models", file=sys.stderr)
        sys.exit(1)

    completed = subprocess.run(
        ["node", "--input-type=module", "-e", SCRIPT, str(path)],
        capture_output=True,
        text=True,
    )
    if completed.returncode != 0:
        print(f"error: could not evaluate {path}: {completed.stderr.strip()}", file=sys.stderr)
        sys.exit(1)

    payload = json.loads(completed.stdout)
    json.dump(payload, sys.stdout, indent=1, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
