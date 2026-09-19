#!/usr/bin/env python3
"""Convert upstream pi's generated model data into ai/models_catalog.json.

Upstream ground truth: pi/packages/ai/src/providers/data/*.json (39 provider
catalogs) + .manifest.json, produced by upstream's own generator:

    cd <upstream clone>/packages/ai && node scripts/generate-models.ts --strict --data-only

Usage:
    python3 scripts/gen_catalog.py /path/to/pi > ai/models_catalog.json

The output mirrors upstream's models.generated.ts MODELS shape:
    {provider: {api: {modelId: model}}}  plus a _meta block carrying the
    manifest's generatedAt timestamp (upstream getBuiltinModelDataGeneratedAt).
"""
import json
import sys
from pathlib import Path


def main() -> None:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    data_dir = Path(sys.argv[1]) / "packages" / "ai" / "src" / "providers" / "data"
    if not data_dir.is_dir():
        print(f"error: {data_dir} not found — run upstream's generate-models first", file=sys.stderr)
        sys.exit(1)

    manifest = json.loads((data_dir / ".manifest.json").read_text())
    providers = {}
    for file in sorted(data_dir.glob("*.json")):
        if file.name.startswith("."):
            continue  # .manifest.json is not a provider catalog
        # provider id = file stem (matches upstream models.generated.ts keys)
        providers[file.stem] = json.loads(file.read_text())

    out = {
        "_meta": {
            "schemaVersion": manifest["schemaVersion"],
            "generatedAt": manifest["generatedAt"],
            "upstreamManifestSHA256": manifest.get("structureHash"),
        },
        "providers": providers,
    }
    json.dump(out, sys.stdout, indent=1, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
