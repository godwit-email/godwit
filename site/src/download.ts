// What /download/<target> sends a browser to: the installer of the version
// that the target's update manifest (update-<target>.json, which
// `mygo build -upload` writes last) names, in the R2 bucket that
// dl.godwit.email serves. The names are those `mygo build` gives the
// installers (cmd/mygo dmg.go, nsis.go and deb.go in mygo v0.7.2); a new mygo
// that renames them breaks these links, which download.test.ts pins.

/** The root of the R2 bucket of updates.s3 in mygo.config.ts, where `mygo build -upload` puts everything. */
export const dl = "https://dl.godwit.email";

export const targets = ["darwin-arm64", "darwin-amd64", "windows-amd64", "windows-arm64", "linux-amd64", "linux-arm64"] as const;

export type Target = (typeof targets)[number];

export function isTarget(s: string): s is Target {
	return (targets as readonly string[]).includes(s);
}

/** The file name of the target's installer for version, e.g. "godwit-1.2.0-arm64.dmg". */
export function installerName(t: Target, version: string): string {
	const [os, arch] = t.split("-");
	switch (os) {
		case "darwin":
			return `godwit-${version}-${arch}.dmg`;
		case "windows":
			return `godwit-setup-${version}-${arch}.exe`;
		default:
			return `godwit_${debVersion(version)}_${arch}.deb`;
	}
}

// debVersion is mygo's: a prerelease sorts before its release in dpkg with "~".
function debVersion(v: string): string {
	v = v.replace(/^v/, "");
	const i = v.indexOf("-");
	if (i >= 0) {
		v = `${v.slice(0, i)}~${v.slice(i + 1).replaceAll("-", ".")}`;
	}
	return v.replaceAll("+", "~");
}
