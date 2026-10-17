import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { copyFileSync, mkdtempSync, readFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const sdk = dirname(fileURLToPath(import.meta.url))
const pkg = JSON.parse(readFileSync(join(sdk, "package.json"), "utf8"))
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
