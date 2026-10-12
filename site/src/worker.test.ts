import { expect, test } from "bun:test";
import worker from "./worker";

const page = `<a data-release data-release-link hidden href="/releases/latest"><span data-version></span></a>
<p data-release hidden><span data-version></span><span data-release-date-wrap>, released <span data-release-date></span></span></p>`;

/** A fake of the bindings, with the paths of the pages asked for. */
interface FakeEnv {
	asked: string[];
	ASSETS: { fetch: (r: Request) => Promise<Response> };
	DL: { get: () => Promise<{ json: () => Promise<object> } | null> };
}

// The built pages, which record the path asked for, and the bucket with an optional manifest.
function env(manifest?: object): FakeEnv {
	const asked: string[] = [];
	return {
		asked,
		ASSETS: {
			fetch: async (r: Request) => {
				asked.push(new URL(r.url).pathname);
				return new Response(page, { headers: { "Content-Type": "text/html", ETag: '"asset"' } });
			},
		},
		DL: { get: async () => (manifest ? { json: async () => manifest } : null) },
	};
}

const get = (path: string, e: FakeEnv, languages?: string) =>
	worker.fetch(new Request(`https://godwit.email${path}`, { headers: languages ? { "Accept-Language": languages } : {} }) as never, e as never);
const released = { version: "1.2.0", date: "2026-10-12T08:00:00Z" };

test("/ is the page of the browser's language, without a redirect", async () => {
	const zh = env();
	const res = await get("/", zh, "zh-CN,zh;q=0.9,en;q=0.8");
	expect(res.status).toBe(200);
	expect(zh.asked).toEqual(["/zh-hans/"]);
	expect(res.headers.get("Vary")).toContain("Accept-Language");
	const en = env();
	await get("/", en, "de-DE,en;q=0.5");
	expect(en.asked).toEqual(["/en/"]);
});

test("a language's path is that language, whatever the browser prefers", async () => {
	const e = env();
	const res = await get("/en/", e, "zh-CN");
	expect(e.asked).toEqual(["/en/"]);
	expect(res.headers.get("Vary") ?? "").not.toContain("Accept-Language");
});

test("without a release the slots stay hidden", async () => {
	expect(await (await get("/en/", env())).text()).toBe(page);
});

test("a release fills the slots and shows them", async () => {
	const res = await get("/en/", env(released));
	const html = await res.text();
	expect(html).not.toContain("hidden");
	expect(html.match(/>v1\.2\.0</g)).toHaveLength(2);
	expect(html).toContain('href="https://github.com/godwit-email/godwit/releases/tag/v1.2.0"');
	expect(html).toContain("October 12, 2026");
	expect(res.headers.get("ETag")).toBeNull();
});

test("a release without a date drops the date", async () => {
	const html = await (await get("/en/", env({ version: "1.2.0" }))).text();
	expect(html).toContain(">v1.2.0<");
	expect(html).not.toContain("released");
});

test("the Simplified Chinese page has a Chinese date", async () => {
	expect(await (await get("/zh-hans/", env(released))).text()).toContain("2026年10月12日");
});

test("downloads still redirect", async () => {
	const res = await get("/download/darwin-arm64", env({ version: "1.2.0" }));
	expect(res.status).toBe(302);
	expect(res.headers.get("Location")).toBe("https://dl.godwit.email/Godwit%201.2.0%20arm64.dmg");
});
