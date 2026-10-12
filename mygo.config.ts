// The app as `mygo build` packages it. The release workflow signs with the
// Developer ID it imports and notarizes (release.yml); elsewhere builds are
// signed ad hoc.
const env = process.env;

export default {
  name: "Godwit",
  identifier: "email.godwit.app",
  version: "0.0.0", // x-release-please-version
  copyright: "© 2026 The Godwit Authors",
  icon: "assets/icon.png",
  main: "./cmd/godwit",
  devUrl: "http://localhost:5173",
  devCommand: "cd web && bun run dev",
  buildCommand: "cd web && bun install --frozen-lockfile && bun run build",
  frontendDist: "web/dist",
  bindings: "web/src/mygo.ts",
  macos: {
    infoPlist: {
      CFBundleIconFile: "Icon",
      CFBundleIconName: "AppIcon",
    },
    signingIdentity: env.MACOS_SIGNING_IDENTITY || "-",
    notarize: env.NOTARY_KEYCHAIN ? { keychainProfile: "notary", keychain: env.NOTARY_KEYCHAIN } : undefined,
  },
  updates: {
    publicKey: "CxE4iKi5zjKGP5eXJEzffin11gMKBzHmLVr5RhAyGqk=",
    github: "godwit-email/godwit",
  },
};
