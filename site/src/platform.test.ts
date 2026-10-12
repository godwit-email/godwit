import { expect, test } from "bun:test";
import { platformForUserAgent } from "./platform";

test("Windows browsers select Windows instead of macOS chrome", () => {
	expect(platformForUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/141.0.0.0 Safari/537.36")).toEqual({ os: "windows", arch: "amd64" });
});

test("Mac browsers keep the macOS title bar", () => {
	expect(platformForUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/26.0 Safari/605.1.15").os).toBe("darwin");
});

test("Linux browsers preserve their architecture, including ARM", () => {
	expect(platformForUserAgent("Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/143.0")).toEqual({ os: "linux", arch: "amd64" });
	expect(platformForUserAgent("Mozilla/5.0 (X11; Linux aarch64) Gecko/20100101 Firefox/143.0")).toEqual({ os: "linux", arch: "arm64" });
});

test("Android is not offered a desktop Linux download", () => {
	expect(platformForUserAgent("Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 Chrome/141.0.0.0 Mobile Safari/537.36").os).not.toBe("linux");
});
