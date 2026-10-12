import { expect, test } from "bun:test";
import { installerName, isTarget } from "./download";

// The names `mygo build` gives the installers it uploads; a link built from a
// wrong one is a 404 on dl.godwit.email.
test("installer names match mygo's", () => {
	expect(installerName("darwin-arm64", "1.2.0")).toBe("Godwit 1.2.0 arm64.dmg");
	expect(installerName("darwin-amd64", "1.2.0")).toBe("Godwit 1.2.0 amd64.dmg");
	expect(installerName("windows-amd64", "1.2.0")).toBe("Godwit Setup 1.2.0 amd64.exe");
	expect(installerName("windows-arm64", "1.2.0")).toBe("Godwit Setup 1.2.0 arm64.exe");
	expect(installerName("linux-amd64", "1.2.0")).toBe("godwit_1.2.0_amd64.deb");
	expect(installerName("linux-arm64", "1.2.0")).toBe("godwit_1.2.0_arm64.deb");
});

test("Debian versions put a prerelease before its release", () => {
	expect(installerName("linux-amd64", "1.2.0-rc-1")).toBe("godwit_1.2.0~rc.1_amd64.deb");
	expect(installerName("linux-amd64", "1.2.0+build")).toBe("godwit_1.2.0~build_amd64.deb");
});

test("only the built platforms are targets", () => {
	expect(isTarget("darwin-arm64")).toBe(true);
	expect(isTarget("darwin-universal")).toBe(false);
	expect(isTarget("update-darwin-arm64.json")).toBe(false);
});
