#!/bin/sh
# latex-paper eval fixture (SPEC §31.12): tectonic build of an authored
# .tex with a ligature-prone word ("efficient") and a real em dash — the
# academic-paper producer class. Provenance script: a maintainer runs it
# once and commits the output; it is never a CI dependency. The tool runs
# in a pinned Docker image so the fixture never depends on host software.
set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SRC="$ROOT/scripts/gen-pdf-fixtures/src"
OUT="$ROOT/internal/ingest/testdata/pdfeval"
IMAGE="dxjoke/tectonic-docker:0.15.0-alpine-biber"

echo "image: $IMAGE"
# The image publishes no arm64 manifest; pin the platform so the script
# also runs on Apple Silicon (under emulation).
docker run --rm --platform linux/amd64 "$IMAGE" tectonic --version
docker run --rm --platform linux/amd64 -v "$SRC":/in -v "$OUT":/out "$IMAGE" \
	tectonic -o /out /in/latex-paper.tex
ls -l "$OUT/latex-paper.pdf"
