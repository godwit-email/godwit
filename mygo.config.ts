// The app as `mygo build` packages it. The release workflow signs with the
// Developer ID it imports and notarizes (release.yml); elsewhere builds are
// signed ad hoc.
const env = process.env;
const provisioningProfile = env.MACOS_PROVISIONING_PROFILE;
const entitlements = env.MACOS_ENTITLEMENTS;
if (Boolean(provisioningProfile) !== Boolean(entitlements)) {
  throw new Error("Set both MACOS_PROVISIONING_PROFILE and MACOS_ENTITLEMENTS for communication notifications");
}

export default {
  name: "Godwit",
  identifier: "email.godwit.app",
  version: "1.3.1", // x-release-please-version
  copyright: "© 2026 The Godwit Authors",
  icon: "assets/icon.png",
  main: "./cmd/godwit",
  devUrl: "http://localhost:5173",
  devCommand: "cd web && bun run dev",
  buildCommand: "cd web && bun install --frozen-lockfile && bun run build",
  frontendDist: "web/dist",
  bindings: "web/src/mygo.ts",
  i18n: { dir: "internal/i18n", source: "en" },
  macos: {
    icon: "assets/AppIcon.icon", // compiled with actool, which needs Xcode 26+; without it the app keeps icon's .icns
    // Set both only for a signed build with an authorized profile (docs/notifications.md).
    provisioningProfile,
    infoPlist: { NSUserActivityTypes: ["INSendMessageIntent"] },
    signingIdentity: env.MACOS_SIGNING_IDENTITY || "-",
    // Native plugins are libraries other developers sign: the hardened runtime
    // loads them only with library validation off (docs/superpowers/specs/2026-10-14-native-plugins-design.md 7.1).
    entitlements: entitlements || "assets/entitlements.plist",
    notarize: env.NOTARY_KEYCHAIN ? { keychainProfile: "notary", keychain: env.NOTARY_KEYCHAIN } : undefined,
  },
  windows: { signCommand: env.WINDOWS_SIGN_COMMAND },
  // Installers and updates live in the R2 bucket that dl.godwit.email serves;
  // installed apps check update-<platform>.json there. `mygo build -upload`
  // puts them in with the R2 token in AWS_ACCESS_KEY_ID and
  // AWS_SECRET_ACCESS_KEY, the manifests last.
  updates: {
    publicKey: "CxE4iKi5zjKGP5eXJEzffin11gMKBzHmLVr5RhAyGqk=",
    url: "https://dl.godwit.email",
    s3: {
      bucket: "godwit-downloads", // Godwit's alone: its files sit at the root, as dl.godwit.email/install.sh
      endpoint: "https://9c578ed9cad837fb4a2564db7b36b483.r2.cloudflarestorage.com",
    },
  },
};
