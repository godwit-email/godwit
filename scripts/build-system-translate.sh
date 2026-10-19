#!/bin/sh
# Builds the system-translation bridge (native/systemtranslate) for both Mac
# architectures into resources/darwin-<arch>/, which `mygo build` and `mygo
# dev` ship in Contents/Resources (a universal app lipos the two) and sign
# with the app. Elsewhere there is nothing to build.
set -eu
[ "$(uname -s)" = Darwin ] || exit 0
cd "$(dirname "$0")/.."
src=native/systemtranslate/SystemTranslate.swift
for arch in arm64 amd64; do
	target=$arch
	[ "$arch" = amd64 ] && target=x86_64
	out=resources/darwin-$arch/libgodwit-translate.dylib
	mkdir -p "$(dirname "$out")"
	# macOS 12 loads it (Translation weak-linked); it translates from macOS 26.
	xcrun swiftc -O -parse-as-library -emit-library -target "$target-apple-macos12" \
		-Xlinker -weak_framework -Xlinker Translation \
		-Xlinker -install_name -Xlinker @rpath/libgodwit-translate.dylib \
		"$src" -o "$out"
done
