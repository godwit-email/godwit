#!/bin/sh
set -eu
[ "$(uname -s)" = Darwin ] || exit 0
cd "$(dirname "$0")/.."
src=native/appleintelligence/AppleIntelligenceHelper.swift

for arch in arm64; do
	out=resources/darwin-$arch/apple-intelligence-helper
	mkdir -p "$(dirname "$out")"
	xcrun swiftc -O -parse-as-library "$src" -o "$out"
	chmod +x "$out"
done
