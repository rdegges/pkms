#!/bin/sh
# encrypted eval fixture (SPEC §31.12): qpdf applies a NON-EMPTY user
# password to the text-bearing word-export fixture (run gen-word-export.sh
# first). The password is fixture data, not a secret — it is recorded in
# manifest.json. 128-bit AES (V=4/R=4), not 256-bit: the incumbent
# ledongthuc/pdf rejects V=5/R=6 as "unsupported encryption" instead of
# ErrInvalidPassword, which would dodge the errPDFEncrypted hint path the
# encrypted class exists to assert. Provenance script: a maintainer runs
# it once and commits the output; it is never a CI dependency. No
# maintained standalone qpdf image exists, so Debian trixie's qpdf runs
# inside the pinned debian:trixie-slim image (never a host tool).
set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/internal/ingest/testdata/pdfeval"
IMAGE="debian:trixie-slim"
USER_PW="pkms-user-pw"
OWNER_PW="pkms-owner-pw"

echo "image: $IMAGE"
docker run --rm -v "$OUT":/out "$IMAGE" sh -c "
	set -eu
	apt-get update -qq >/dev/null
	apt-get install -y -qq qpdf >/dev/null
	qpdf --version
	qpdf --encrypt $USER_PW $OWNER_PW 128 --use-aes=y -- \
		/out/word-export.pdf /out/encrypted.pdf
"
ls -l "$OUT/encrypted.pdf"
