// godwit.email: the static pages of dist; the home pages, which get the newest
// release written in, at / in the browser's language and at each language's
// fixed path; and /download/<target>, which redirects to the newest installer
// of the target (wrangler.jsonc runs this Worker first only for those paths).
import { dl, installerName, isTarget } from "./download";
import { type Lang, locales, paths, pick } from "./locales";

interface Env {
	ASSETS: Fetcher;
	DL: R2Bucket;
}

/** What the home pages show of a release, from its update manifest. */
interface Release {
	version: string;
	date?: string;
}

const releases = "https://github.com/godwit-email/godwit/releases";

/** The newest release: every platform gets the same version, and macOS on Apple silicon is built first. */
async function latest(env: Env): Promise<Release | null> {
	const manifest = await env.DL.get("update-darwin-arm64.json");
	return manifest ? manifest.json<Release>() : null;
}

/** The home page with the newest release written into its [data-release…] slots, which stay hidden without one. */
function withRelease(page: Response, r: Release | null, lang: Lang): Response {
	if (!r) return page;
	const date = r.date && new Date(r.date);
	const day =
		date && !Number.isNaN(date.getTime())
			? new Intl.DateTimeFormat(lang, { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" }).format(date)
			: undefined;
	return new HTMLRewriter()
		.on("[data-release]", { element: (el) => void el.removeAttribute("hidden") })
		.on("[data-version]", { element: (el) => void el.setInnerContent(`v${r.version}`) })
		.on("[data-release-link]", { element: (el) => void el.setAttribute("href", `${releases}/tag/v${encodeURIComponent(r.version)}`) })
		.on("[data-release-date]", { element: (el) => void el.setInnerContent(day ?? "") })
		.on("[data-release-date-wrap]", { element: (el) => void (day ? undefined : el.remove()) })
		.transform(page);
}

export default {
	async fetch(req, env): Promise<Response> {
		const url = new URL(req.url);
		const fixed = locales.find((l) => paths[l] === url.pathname);
		if (url.pathname === "/" || fixed) {
			// "/" is the page of the browser's language, at "/": no redirect.
			const lang = fixed ?? pick(req.headers.get("Accept-Language"));
			const page = await env.ASSETS.fetch(new Request(new URL(paths[lang], url), req));
			if (!page.ok) return page;
			const out = withRelease(page, await latest(env), lang);
			const headers = new Headers(out.headers);
			headers.delete("ETag"); // it changes with each release, and / with the language
			headers.set("Cache-Control", "no-cache");
			if (!fixed) headers.append("Vary", "Accept-Language");
			return new Response(out.body, { status: out.status, headers });
		}
		const path = url.pathname;
		const target = path.match(/^\/download\/([^/]+)\/?$/)?.[1];
		if (!target || !isTarget(target)) {
			return env.ASSETS.fetch(req); // the 404 page
		}
		const manifest = await env.DL.get(`update-${target}.json`);
		if (!manifest) {
			return new Response("Godwit 还没有发布这个平台的版本。\nNo release of Godwit for this platform yet.\n", {
				status: 404,
				headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store" },
			});
		}
		const { version } = await manifest.json<{ version: string }>();
		return new Response(null, {
			status: 302,
			headers: { Location: `${dl}/${encodeURIComponent(installerName(target, version))}`, "Cache-Control": "no-store" },
		});
	},
} satisfies ExportedHandler<Env>;
