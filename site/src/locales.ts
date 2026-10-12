// The site's languages (BCP 47), each at a fixed path; / is whichever the
// browser prefers (src/worker.ts). Adding zh-Hant is a row here, its strings
// and demo, a page in src/pages, and its path in wrangler.jsonc's run_worker_first.

export const locales = ["zh-Hans", "en"] as const;

export type Lang = (typeof locales)[number];

/** Each language's fixed path: lower case, as URL paths are case-sensitive. */
export const paths: Record<Lang, string> = { "zh-Hans": "/zh-hans/", en: "/en/" };

/** Each language's name in itself, for the language switch. */
export const names: Record<Lang, string> = { "zh-Hans": "简体中文", en: "English" };

/** For a browser that prefers none of ours. */
const fallback: Lang = "en";

/** Ours for one tag of Accept-Language, or undefined. */
function match(tag: string): Lang | undefined {
	const [language, ...rest] = tag.toLowerCase().split("-");
	if (language === "en") return "en";
	if (language !== "zh") return undefined;
	// Traditional by script, or by the regions that write it; Simplified until there is a zh-Hant.
	const traditional = rest.some((s) => s === "hant" || s === "tw" || s === "hk" || s === "mo");
	const want = traditional ? "zh-Hant" : "zh-Hans";
	return (locales as readonly string[]).includes(want) ? (want as Lang) : "zh-Hans";
}

/** The language for an Accept-Language header: its most preferred tag we have. */
export function pick(acceptLanguage: string | null): Lang {
	const ranked = (acceptLanguage ?? "")
		.split(",")
		.map((part, i) => {
			const [tag, ...params] = part.trim().split(";");
			const q = params.map((p) => p.trim().match(/^q=([\d.]+)$/)?.[1]).find(Boolean);
			return { tag: tag.trim(), q: q === undefined ? 1 : Number(q), i };
		})
		.filter((t) => t.tag && t.q > 0)
		.sort((a, b) => b.q - a.q || a.i - b.i);
	for (const t of ranked) {
		const lang = match(t.tag);
		if (lang) return lang;
	}
	return fallback;
}
