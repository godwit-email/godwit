/** The browser's desktop platform, shared by the download button and demo chrome. */
export interface BrowserPlatform {
	os: "darwin" | "windows" | "linux";
	arch: "arm64" | "amd64";
}

/**
 * Match the existing download defaults: Macs use Apple silicon because the
 * browser does not identify the chip; Windows usually reports x64 even on
 * ARM, where the x64 build runs too. Android is not a desktop Linux browser.
 */
export function platformForUserAgent(userAgent: string): BrowserPlatform {
	const os = /Windows/.test(userAgent) ? "windows" : /Linux|X11/.test(userAgent) && !/Android/.test(userAgent) ? "linux" : "darwin";
	return { os, arch: os === "darwin" || /aarch64|arm64/i.test(userAgent) ? "arm64" : "amd64" };
}
