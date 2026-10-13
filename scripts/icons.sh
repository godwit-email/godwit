#!/bin/sh
# Builds the app icons from assets/logo.svg. It copies the logo into
# assets/AppIcon.icon (Icon Composer), whose layer it is: actool follows no link;
# `mygo build` compiles the .icon (macos.icon). scripts/winicon.swift draws
# assets/icon.png, mygo.config.ts's icon. Run it after changing the icon, and
# commit the output.
set -e
cd "$(dirname "$0")/.."
cp assets/logo.svg assets/AppIcon.icon/Assets/logo.svg
swift scripts/winicon.swift
ls -l assets/icon.png
