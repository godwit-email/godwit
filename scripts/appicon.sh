#!/bin/sh
# Compiles assets/AppIcon.icon (Icon Composer) into the macOS icon that
# resources/darwin ships: Assets.car, and Icon.icns for older macOS (mygo
# keeps the name AppIcon.icns for its own icon).
# Needs Xcode 26+. Run it after changing the icon, and commit the output.
# shortcut: until mygo compiles .icon itself (egoist/mygo#173), then use macos.icon and drop this.
set -e
cd "$(dirname "$0")/.."
xcrun actool assets/AppIcon.icon --compile resources/darwin --app-icon AppIcon \
	--platform macosx --minimum-deployment-target 12.0 \
	--output-partial-info-plist /dev/null --output-format human-readable-text >/dev/null
mv resources/darwin/AppIcon.icns resources/darwin/Icon.icns
ls -l resources/darwin
