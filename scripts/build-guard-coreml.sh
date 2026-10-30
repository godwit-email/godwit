#!/bin/sh
# Builds AstrLink Guard's CoreML bridge (native/guardcoreml) for both Mac
# architectures into resources/darwin-<arch>/, which `mygo build` and `mygo
# dev` ship in Contents/Resources. Elsewhere there is nothing to build.
set -eu
[ "$(uname -s)" = Darwin ] || exit 0
cd "$(dirname "$0")/.."
src=native/guardcoreml/GuardCoreML.swift
for arch in arm64 amd64; do
	target=$arch
	[ "$arch" = amd64 ] && target=x86_64
	out=resources/darwin-$arch/libgodwit-guard-coreml.dylib
	mkdir -p "$(dirname "$out")"
	# CoreML programs need macOS 13; the compute plan, 14.4, is checked at run time.
	xcrun swiftc -O -parse-as-library -emit-library -target "$target-apple-macos13" \
		-Xlinker -install_name -Xlinker @rpath/libgodwit-guard-coreml.dylib \
		"$src" -o "$out"
done
