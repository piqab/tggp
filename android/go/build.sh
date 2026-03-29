#!/usr/bin/env bash
# ──────────────────────────────────────────────────────────────────────────────
# build.sh — Compile the Go hysteria2 package into an Android AAR via gomobile.
#
# Prerequisites:
#   • Go 1.23+
#   • Android SDK + NDK (set ANDROID_HOME / ANDROID_NDK_HOME)
#   • gomobile installed:
#       go install golang.org/x/mobile/cmd/gomobile@latest
#       gomobile init
#
# Usage:
#   cd android/go
#   ./build.sh
#
# Output:
#   ../app/libs/hysteria2.aar   (copied automatically)
# ──────────────────────────────────────────────────────────────────────────────
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../app/libs"
AAR_NAME="hysteria2.aar"

echo "==> Ensuring gomobile is available…"
if ! command -v gomobile &>/dev/null; then
    echo "ERROR: gomobile not found. Install it with:"
    echo "  go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init"
    exit 1
fi

echo "==> Fetching golang.org/x/mobile…"
cd "${SCRIPT_DIR}"
go get golang.org/x/mobile@latest

echo "==> Tidying modules…"
go mod tidy

echo "==> Building Android AAR (targets: arm, arm64, 386, amd64)…"
gomobile bind \
    -target=android \
    -androidapi=21 \
    -o "${SCRIPT_DIR}/${AAR_NAME}" \
    -v \
    .

echo "==> Copying ${AAR_NAME} → ${OUTPUT_DIR}/"
mkdir -p "${OUTPUT_DIR}"
cp "${SCRIPT_DIR}/${AAR_NAME}" "${OUTPUT_DIR}/${AAR_NAME}"
# Also copy the sources JAR produced by gomobile (same base name, -sources suffix)
SOURCES_JAR="${SCRIPT_DIR}/hysteria2-sources.jar"
if [[ -f "${SOURCES_JAR}" ]]; then
    cp "${SOURCES_JAR}" "${OUTPUT_DIR}/hysteria2-sources.jar"
fi

echo ""
echo "✓ Build complete!"
echo "  ${OUTPUT_DIR}/${AAR_NAME}"
echo ""
echo "Next steps:"
echo "  1. Open android/ in Android Studio."
echo "  2. Sync Gradle — it will pick up app/libs/hysteria2.aar automatically."
echo "  3. Build & run the app."
