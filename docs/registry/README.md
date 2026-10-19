# Godwit plugin registry

This is the template for the `godwit-email/plugins` repository: the list of plugins Godwit's
Extensions page offers as reviewed. Copy `registry.json` to the repository root and
`publish.yml` to `.github/workflows/`. The design is
`docs/superpowers/specs/2026-10-14-native-plugins-design.md` in the Godwit repository.

## How it works

- `registry.json` lists plugins and their releases. Each release carries its `plugin.json`
  (as `manifest`) and, per file it ships, the URL, size and SHA-256. Godwit trusts **those
  exact files**, never what a plugin says about itself.
- A merge to `main` runs `godwit-registry check --fetch` (the validator Godwit itself uses,
  plus a download of every file), signs `registry.json` with the repository's ed25519 key
  and uploads both files to `dl.godwit.email/plugins/`. Godwit verifies the signature with the
  public key built into it, and refuses a registry older than the one it has.
- Godwit installs a release by downloading its file and checking the size and SHA-256. A
  mismatch is discarded; nothing lets a user install it anyway.
- A **native** plugin (a shared library) runs with all of Godwit's rights. A listed native
  file is installed and enabled after one confirmation. A **script** plugin runs in Godwit's
  sandbox; being listed does not grant its permissions, the user still allows them.

## Adding a plugin or a release

Open a pull request that adds to `plugins` (or to an existing plugin's `releases`):

```json
{
  "name": "dlx",
  "title": { "default": "DLX", "zh-CN": "DLX 翻译" },
  "summary": { "default": "Translate mail with DeepLX. Uses an unofficial DeepL endpoint that may stop working." },
  "homepage": "https://github.com/someone/godwit-dlx",
  "releases": [
    {
      "version": "0.1.0",
      "minGodwit": "1.2.0",
      "manifest": { "name": "dlx", "version": "0.1.0", "native": { "abi": 1 }, "capabilities": ["translate"], "config": [] },
      "artifacts": {
        "darwin-arm64": { "url": "https://github.com/someone/godwit-dlx/releases/download/v0.1.0/dlx-darwin-arm64.dylib", "sha256": "…", "size": 11355106 }
      }
    }
  ]
}
```

- Native releases name each file by platform: `darwin-arm64`, `darwin-amd64`, `windows-amd64`,
  `windows-arm64`, `linux-amd64`, `linux-arm64`. A missing platform is simply not offered there.
  The file is the bare library (no archive), at most 64 MiB, exporting the C ABI in
  `sdk/native/godwit_plugin.h`.
- A script release has one file, `any`: the plugin bundled into the single `.js` file its
  `manifest.main` names, at most 5 MiB.
- A native release may list `assets`: large files Godwit downloads and checks itself and only
  then hands the plugin, such as a model's weights or an inference engine's libraries. Each
  asset is a group with one or more variants (one variant: fixed, it comes with every model;
  several: a model of the plugin picks one). `platforms` (optional) limits a group to some
  platforms, at most one group of a name per platform. Each file has a plain `name` (no
  directories), an https `url`, `sha256` and `size` (up to 64 GiB). `minMemoryGB` is the
  physical memory a variant needs, a number or by platform (`{"default": 6, "windows-*": 8}`:
  exact platform, then `<os>-*`, then `default`); measure it on each platform, since Windows
  commits memory macOS and Linux only reserve:

  ```json
  "assets": [
    { "name": "engine", "platforms": ["darwin-arm64"], "variants": [
      { "id": "b11707-metal", "files": [{ "name": "libllama.0.dylib", "url": "…", "sha256": "…", "size": 3529184 }] }] },
    { "name": "model", "variants": [
      { "id": "4b-q4", "minMemoryGB": 8, "files": [{ "name": "model.gguf", "url": "…", "sha256": "…", "size": 2710000000 }] },
      { "id": "2b-q8", "minMemoryGB": { "default": 6, "windows-*": 8 }, "files": [{ "name": "model.gguf", "url": "…", "sha256": "…", "size": 2010000000 }] }] }
  ]
  ```

  A release's assets are part of what was reviewed: changing or removing any of them, even a
  variant nobody downloaded, revokes the release for everyone who installed it.
- Bump the top-level `updated` (RFC 3339) in every pull request.
- **Never edit a published release**; publish a new version instead. A Godwit that cannot read a
  release (a field, capability or manifest field from a newer Godwit) sets that release aside and
  keeps the rest of the registry, so a release that uses anything new must carry the `minGodwit`
  that reads it. Editing a release older Godwits installed into a form they cannot read revokes
  it for them. `check` reads every release with the Godwit it is built from and fails on anything
  it cannot read.

## Review checklist

Before approving:

1. **Source.** Read the code, or know who builds it and from what. Prefer releases built by the
   author's public CI from a tagged commit, so the file can be traced to its source.
2. **Files.** `check --fetch` passes: every file, assets included, is at its URL with the size
   and SHA-256 listed. URLs should be immutable (a release asset, not a branch). Assets that are
   third-party builds (an engine, model weights) are pinned to an exact upstream version and
   mirrored, so an upstream deletion cannot break installs.
3. **Manifest.** It is the plugin's real `plugin.json`. For a native plugin, its `describe`
   answers the same name, version, ABI and capabilities, or Godwit will not start it.
   For a script plugin, the permissions are no wider than what it does.
4. **Risk in plain words.** If the plugin depends on an unofficial or rate-limited service, or
   sends mail content somewhere, `summary` says so.

## Revoking

Remove the release (or the whole plugin) and merge. On its next refresh (on opening
Extensions, and every 24 hours) Godwit stops every installed copy of a file the registry no
longer lists and asks the user before it runs again.

## Keys

- `go run github.com/godwit-email/godwit/cmd/godwit-registry@main keygen` prints a key pair.
- The private key goes to the repository secret `GODWIT_REGISTRY_KEY`, and nowhere else.
- The public key goes into `registryPublicKey` in Godwit's `core/registry.go`. Godwit's plugin
  directory is off while that constant is empty.
- Rotating the key needs a Godwit release with the new public key; publish with the old key
  until that release is out.

Repository secrets: `GODWIT_REGISTRY_KEY`, and `R2_ACCESS_KEY_ID` and `R2_SECRET_ACCESS_KEY`
(the same R2 token Godwit's release workflow uses).

## Trying a release locally

Flows only the registry offers (a plugin's hosted model files) can be tried in a development
build before anything is published. `godwit-registry dev spec.json` signs a registry with a key
of its own and serves it, with its files, on https://127.0.0.1:8743. The spec names each
plugin's built directory and, per asset variant, a directory of files (see
plugin-startlux's `scripts/dev-registry.json`). It prints the command to run the app with:

    GODWIT_DEV_REGISTRY=<spec dir>/.dev-registry/source.json mise run dev

Production builds ignore `GODWIT_DEV_REGISTRY`.
