import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { copyFileSync, mkdtempSync, readFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const sdk = dirname(fileURLToPath(import.meta.url))
const pkg = JSON.parse(readFileSync(join(sdk, "package.json"), "utf8"))
const appVersion = readFileSync(join(sdk, "../mygo.config.ts"), "utf8").match(/version: "([^"]+)", \/\/ x-release-please-version/)?.[1]
const release = JSON.parse(readFileSync(join(sdk, "../.release-please-manifest.json"), "utf8"))
const lock = JSON.parse(readFileSync(join(sdk, "package-lock.json"), "utf8"))
assert.equal(pkg.version, appVersion, "SDK and app versions must match")
assert.equal(pkg.version, release["."], "SDK and release versions must match")
if (process.env.GODWIT_RELEASE_TAG) assert.equal(process.env.GODWIT_RELEASE_TAG, `v${pkg.version}`, "App release tag and SDK version must match")
assert.equal(lock.version, pkg.version)
assert.equal(lock.packages[""].version, pkg.version)
assert.equal(pkg.name, "@godwit/sdk")
assert.equal(pkg.types, "./godwit.d.ts")
assert.deepEqual(pkg.exports, { ".": { types: "./godwit.d.ts" } })
assert.equal(pkg.dependencies, undefined)

const consumer = mkdtempSync(join(tmpdir(), "godwit-sdk-"))
try {
  const [packed] = JSON.parse(execFileSync("npm", ["pack", "--json", "--ignore-scripts", "--pack-destination", consumer], { cwd: sdk, encoding: "utf8" }))
  assert.deepEqual(packed.files.map(f => f.path).sort(), ["LICENSE", "README.md", "godwit.d.ts", "package.json"])
  execFileSync("npm", ["install", "--ignore-scripts", "--no-audit", "--no-fund", "--package-lock=false", join(consumer, packed.filename)], { cwd: consumer, stdio: "inherit" })
  copyFileSync(join(sdk, "../core/plugins/testdata/sdk-types/index.ts"), join(consumer, "index.ts"))
  execFileSync(process.execPath, [join(sdk, "node_modules/typescript/bin/tsc"), "--noEmit", "--strict", "--target", "ES2022", "--module", "NodeNext", "index.ts"], { cwd: consumer, stdio: "inherit" })
} finally {
  rmSync(consumer, { recursive: true, force: true })
}
