// The app as `mygo build` packages it. The release workflow signs with the
// Developer ID it imports and notarizes (release.yml); elsewhere builds are
// signed ad hoc.
const env = process.env;

export default {
  name: "Godwit",
  identifier: "email.godwit.app",
  version: "0.1.0", // x-release-please-version
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
