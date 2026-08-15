#!/bin/sh
# chrome-print eval fixture (SPEC §31.12): headless Chromium print-to-PDF
# of an authored HTML page — the browser-print producer class, which emits
# Identity-H subset fonts. Provenance script: a maintainer runs it once and
# commits the output; it is never a CI dependency. The tool runs in a
# pinned Docker image so the fixture never depends on host software.
set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SRC="$ROOT/scripts/gen-pdf-fixtures/src"
OUT="$ROOT/internal/ingest/testdata/pdfeval"
IMAGE="zenika/alpine-chrome:124"

echo "image: $IMAGE"
# The image entrypoint injects launch flags that break a bare --version;
# call the browser binary directly for the version line.
docker run --rm --entrypoint /usr/bin/chromium-browser "$IMAGE" --version
docker run --rm -v "$SRC":/in -v "$OUT":/out "$IMAGE" \
	--headless --no-sandbox --no-zygote --disable-gpu \
	--print-to-pdf=/out/chrome-print.pdf file:///in/chrome-print.html
ls -l "$OUT/chrome-print.pdf"
