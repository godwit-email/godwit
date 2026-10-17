# @godwit/sdk

Type definitions for Godwit plugins. The host supplies the APIs at runtime;
this package contains no JavaScript and has no runtime dependencies.

```sh
npm install --save-dev @godwit/sdk
```

```ts
import type { Ctx } from "@godwit/sdk"

export default function (ctx: Ctx) {
  ctx.models.register({ listModels: () => ({ models: [] }) })
}
```

The npm package version matches the Godwit application version. Installing newer
types does not upgrade the host; plugins must still declare their host
compatibility in the registry.
The native C header remains available in this repository under `sdk/native/`.

## Check and release

In `sdk/`, run `npm ci && npm test`. The test packs the real npm tarball,
installs it into an isolated consumer, and type-checks the existing SDK example
with strict TypeScript. It also checks that the tarball contains only the
declarations, metadata, README, and license.

The `SDK` workflow checks pull requests and pushes to `main`, including version
agreement with the app, release metadata, and package lock. Release-please updates
both app and SDK versions in the same release PR. Once the app release is public,
the `Release` workflow checks out that same `vX.Y.Z` tag, tests the package, and
publishes `@godwit/sdk@X.Y.Z` to npm with provenance. There is no separate SDK tag.
Retry a failed SDK job from the app's release run.

For the first publication, an npm maintainer with access to the `@godwit`
scope must configure the repository's `NPM_TOKEN` secret with publish access
(including permission to publish a new public package). This workflow does
not create an npm organization or grant package permissions.

After the package exists, configure its npm trusted publisher for GitHub Actions:
owner `godwit-email`, repository `godwit`, workflow `release.yml`, no environment.
Then remove `NPM_TOKEN`; subsequent releases use OIDC without a long-lived token.
Trusted publishing requires npm >=11.5.1; the workflow installs npm 11 explicitly.
