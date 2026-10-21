// The website's Umami, and only the website's: the app has no analytics. The
// pages load its script (events are data-umami-event attributes, or track()),
// and src/worker.ts counts /download redirects itself. Events carry fixed
// values only: no text anyone typed or copied, no user agent, no IP.

export const umami = {
	origin: "https://u.baran.wang",
	website: "b1bc7048-78d5-4b52-927e-bd85528aaab1",
	/** The one host counted, so local previews stay out of the stats. */
	domain: "godwit.email",
};

/** Records an event when the script has loaded; a blocker that stops it changes nothing on the page. */
export function track(name: string, data?: Record<string, string>): void {
	(globalThis as { umami?: { track: (name: string, data?: Record<string, string>) => void } }).umami?.track(name, data);
}

/**
 * Counts one redirect of /download/<target> as the event "download", from the
 * Worker: it also counts links from outside the site (the README, release
 * notes) and browsers whose blockers stop the script. Sends the referrer's
 * origin and the browser's user agent, which Umami needs to drop bots, but
 * not the visitor's IP.
 */
export function countDownload(req: Request, target: string): Promise<unknown> {
	let referrer = "";
	try {
		referrer = new URL(req.headers.get("Referer") ?? "").origin;
	} catch {}
	return fetch(`${umami.origin}/api/send`, {
		method: "POST",
		headers: { "Content-Type": "application/json", "User-Agent": req.headers.get("User-Agent") ?? "" },
		body: JSON.stringify({
			type: "event",
			payload: {
				website: umami.website,
				hostname: umami.domain,
				url: `/download/${target}`,
				referrer,
				language: req.headers.get("Accept-Language")?.split(",")[0] ?? "",
				name: "download",
				data: { target },
			},
		}),
	}).catch(() => {});
}
