#!/usr/bin/env bash
# Builds the Android library (AAR) from ./mobile with gomobile.
#
# Requires an Android SDK (ANDROID_HOME, with a platforms/android-* jar) and
# NDK (ANDROID_NDK_HOME, or ndk/<version> inside the SDK), plus a JDK.
#
#   scripts/build-aar.sh [output.aar]
#
# It builds for arm64 only, which covers the supported phones (Pixel 7 and
# newer are 64-bit ARM only). Set TARGETS to add others, e.g.
# TARGETS=android/arm64,android/amd64 for an x86_64 emulator.
#
# gomobile and gobind are built from the versions pinned in go.mod, so local
# and CI builds match. ("gomobile init" is not needed, and would install the
# latest gobind instead.)
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$root/android/app/libs/franklinwh.aar}
targets=${TARGETS:-android/arm64}
androidapi=${ANDROID_API:-33}

cd "$root"
bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
GOBIN=$bin go install golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind
export PATH="$bin:$PATH"

mkdir -p "$(dirname "$out")"
gomobile bind \
	-target="$targets" \
	-androidapi="$androidapi" \
	-javapkg=com.github.lanrat.franklinwh \
	-trimpath \
	-ldflags="-s -w" \
	-o "$out" \
	./mobile
echo "built $out"
