#!/bin/sh
# scan-image-only eval fixture (SPEC §31.12): a PDF whose single page is
# ONLY a raster image, no text layer — the scanned-document class, which
# must yield the honest "no extractable text" outcome (OCR is a non-goal).
# Provenance script: a maintainer runs it once and commits the output; it
# is never a CI dependency. The tool runs in a pinned Docker image so the
# fixture never depends on host software.
set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/internal/ingest/testdata/pdfeval"
IMAGE="dpokidov/imagemagick:7.1.1-47"

echo "image: $IMAGE"
docker run --rm "$IMAGE" -version
# A deterministic letter-size gradient with page-like blocks; ImageMagick
# embeds it as an image XObject with zero text operators.
docker run --rm -v "$OUT":/out "$IMAGE" \
	-size 612x792 gradient:white-lightgray \
	-fill gray -draw "rectangle 72,72 540,120" \
	-draw "rectangle 72,150 540,700" \
	/out/scan-image-only.pdf
ls -l "$OUT/scan-image-only.pdf"
