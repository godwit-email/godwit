#!/bin/sh
# Builds the Apple Intelligence helper (native/appleintelligence) for both Mac
# architectures into resources/darwin-<arch>/, which `mygo build` and `mygo
# dev` ship in Contents/Resources (a universal app lipos the two) and sign
# with the app. Elsewhere there is nothing to build.
set -eu
[ "$(uname -s)" = Darwin ] || exit 0
cd "$(dirname "$0")/.."
src=native/appleintelligence/AppleIntelligenceHelper.swift
for arch in arm64 amd64; do
	target=$arch
	[ "$arch" = amd64 ] && target=x86_64
	out=resources/darwin-$arch/apple-intelligence-helper
	mkdir -p "$(dirname "$out")"
	# FoundationModels is macOS 26+: on older systems the helper fails to start, which reads as unavailable.
	xcrun swiftc -O -parse-as-library -target "$target-apple-macos26" "$src" -o "$out"
done
