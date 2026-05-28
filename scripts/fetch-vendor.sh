#!/usr/bin/env sh
# Downloads the third-party JS/CSS that the embedded UI depends on.
# Run once before `go build`, and re-run to bump versions.

set -eu

DEST="internal/server/static"
mkdir -p "$DEST"

HTMX_VERSION="${HTMX_VERSION:-2.0.3}"
ALPINE_VERSION="${ALPINE_VERSION:-3.14.1}"
TAILWIND_VERSION="${TAILWIND_VERSION:-3.4.4}"

fetch() {
    url="$1"
    out="$2"
    echo "  → $out"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$out"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$url" -O "$out"
    else
        echo "neither curl nor wget available" >&2
        exit 1
    fi
}

echo "Fetching htmx ${HTMX_VERSION}"
fetch "https://unpkg.com/htmx.org@${HTMX_VERSION}/dist/htmx.min.js" "$DEST/htmx.min.js"

echo "Fetching Alpine.js ${ALPINE_VERSION}"
fetch "https://cdn.jsdelivr.net/npm/alpinejs@${ALPINE_VERSION}/dist/cdn.min.js" "$DEST/alpine.min.js"

echo "Fetching Tailwind Play CDN ${TAILWIND_VERSION}"
# Note: Tailwind Play CDN is a JS bundle that injects styles dynamically;
# we serve it as tailwind.js even though our layout references tailwind.css
# (we keep the same path so a future swap to a real Tailwind build is a no-op).
fetch "https://cdn.tailwindcss.com/${TAILWIND_VERSION}" "$DEST/tailwind.js"
# Also write an empty tailwind.css to avoid a 404 from the <link> tag —
# real styles are injected by tailwind.js at runtime.
: > "$DEST/tailwind.css"

echo "Done. Vendored assets:"
ls -lh "$DEST"
