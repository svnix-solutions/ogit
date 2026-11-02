"""Keeps the editor to itself: no telemetry, and webviews served from the
workspace's own origin instead of vscode-cdn.net."""
import json
import sys

path = sys.argv[1]
with open(path) as f:
    product = json.load(f)
product["enableTelemetry"] = False
for key in ("webviewContentExternalBaseUrlTemplate", "aiConfig"):
    product.pop(key, None)
with open(path, "w") as f:
    json.dump(product, f, indent=2)
