#!/bin/sh
# Builds the app icons from assets/logo.svg. It first copies the logo into
# assets/AppIcon.icon (Icon Composer), whose layer it is: actool follows no link.
# macOS: actool compiles the .icon into resources/darwin: Assets.car, and
# Icon.icns for older macOS (mygo keeps AppIcon.icns for its own icon).
# Windows: scripts/winicon.swift draws assets/icon.png, mygo.config.ts's icon.
# Needs Xcode 26+. Run it after changing the icon, and commit the output.
# shortcut: macOS until mygo compiles .icon itself (egoist/mygo#173); then use macos.icon and drop resources/darwin.
set -e
cd "$(dirname "$0")/.."
cp assets/logo.svg assets/AppIcon.icon/Assets/logo.svg
xcrun actool assets/AppIcon.icon --compile resources/darwin --app-icon AppIcon \
	--platform macosx --minimum-deployment-target 12.0 \
	--output-partial-info-plist /dev/null --output-format human-readable-text >/dev/null
mv resources/darwin/AppIcon.icns resources/darwin/Icon.icns
swift scripts/winicon.swift
ls -l resources/darwin assets/icon.png
