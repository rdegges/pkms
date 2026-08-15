#!/bin/sh
# word-export eval fixture (SPEC §31.12): a LibreOffice headless export of
# an authored .fodt — the "Word/Writer export" producer class, which emits
# Identity-H subset fonts. Provenance script: a maintainer runs it once and
# commits the output; it is never a CI dependency. The tool runs in a
# pinned Docker image so the fixture never depends on host software.
set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SRC="$ROOT/scripts/gen-pdf-fixtures/src"
OUT="$ROOT/internal/ingest/testdata/pdfeval"
IMAGE="lscr.io/linuxserver/libreoffice:25.8.7.3-r0-ls205"

echo "image: $IMAGE"
# The image is a GUI (KasmVNC) image; bypass its init and run the CLI
# directly. HOME must be writable for the LibreOffice profile.
docker run --rm --entrypoint /usr/bin/libreoffice -e HOME=/tmp "$IMAGE" --version
docker run --rm --entrypoint /usr/bin/libreoffice -e HOME=/tmp \
	-v "$SRC":/in -v "$OUT":/out "$IMAGE" \
	--headless --convert-to pdf --outdir /out /in/word-export.fodt
ls -l "$OUT/word-export.pdf"
