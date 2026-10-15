// /og/<lang>.png, the image links to godwit.email show (1200×630), one for
// each language: the mark and name, small; the headline of the home page; and
// the app's window, in its light tokens, rising from the bottom edge as on the
// page. Rendered at build time with resvg: Geist, and Noto Sans SC for Chinese
// (Geist has no CJK glyphs, and the build runs where no system font has them).
import { join } from "node:path";
import { Resvg } from "@resvg/resvg-js";
import type { APIRoute, GetStaticPaths } from "astro";
import logo from "../../assets/logo.svg?raw";
import { strings } from "../../i18n";
import { type Lang, locales, paths } from "../../locales";

const modules = join(process.cwd(), "node_modules");
// Paths, not buffers: resvg-js 2.6 takes font files only.
const fonts = [
	"geist/dist/fonts/geist-sans/Geist-Medium.ttf",
	"geist/dist/fonts/geist-sans/Geist-SemiBold.ttf",
	"@expo-google-fonts/noto-sans-sc/500Medium/NotoSansSC_500Medium.ttf",
	"@expo-google-fonts/noto-sans-sc/600SemiBold/NotoSansSC_600SemiBold.ttf",
].map((f) => join(modules, f));
const family = "Geist, Noto Sans SC";

// The light tokens of webui.ThemeVars(theme.Light()), as in styles.css.
const t = { window: "#ededed", background: "#ffffff", foreground: "#0a0a0a", muted: "#737373", border: "#e5e5e5", sidebar: "#fafafa", accent: "#f5f5f5", unread: "#3b82f6" };

const bird = logo.match(/<path[^>]*\/>/g)?.join("") ?? "";

const escape = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");

/** A message row of the drawn list: a sender and two lines, as bars. */
const row = (y: number, sender: number, line: number, unread: boolean, selected: boolean) => `
	${selected ? `<rect x="104" y="${y - 12}" width="276" height="68" rx="8" fill="${t.accent}"/>` : ""}
	${unread ? `<circle cx="112" cy="${y + 4}" r="3.5" fill="${t.unread}"/>` : ""}
	<rect x="122" y="${y}" width="${sender}" height="9" rx="4.5" fill="${t.foreground}" opacity="${unread ? 0.85 : 0.55}"/>
	<rect x="122" y="${y + 18}" width="${line}" height="7" rx="3.5" fill="${t.foreground}" opacity="0.35"/>
	<rect x="122" y="${y + 33}" width="${line - 40}" height="7" rx="3.5" fill="${t.muted}" opacity="0.35"/>`;

function svg(lang: Lang): string {
	// Resvg has no text-autospace: put back the CJK-Latin gap the page's CSS draws.
	const [first, second] = strings[lang].headline.map((l) => l.replace(/(?<=[\u4e00-\u9fff])(?=[A-Za-z0-9])|(?<=[A-Za-z0-9])(?=[\u4e00-\u9fff])/g, " "));
	return `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630">
	<rect width="1200" height="630" fill="${t.window}"/>

	<g transform="translate(72 50) scale(0.0703)" fill="${t.foreground}">${bird}</g>
	<text x="118" y="83" font-family="${family}" font-weight="600" font-size="28" letter-spacing="-0.5" fill="${t.foreground}">Godwit</text>
	<text x="1128" y="83" text-anchor="end" font-family="${family}" font-weight="500" font-size="22" fill="${t.muted}">godwit.email</text>

	<text font-family="${family}" font-weight="600" font-size="62" letter-spacing="-1.5" fill="${t.foreground}">
		<tspan x="72" y="196">${escape(first.trim())}</tspan>
		<tspan x="72" y="274">${escape(second)}</tspan>
	</text>

	<!-- The window, rising from the bottom edge. -->
	<rect x="72" y="336" width="1176" height="340" rx="20" fill="${t.background}" opacity="0.55"/>
	<rect x="88" y="352" width="308" height="320" rx="14" fill="${t.background}" stroke="${t.border}"/>
	${row(386, 92, 220, true, false)}
	${row(462, 128, 230, false, true)}
	${row(538, 104, 214, false, false)}

	<rect x="408" y="352" width="840" height="320" rx="14" fill="${t.background}" stroke="${t.border}"/>
	<rect x="440" y="384" width="300" height="16" rx="8" fill="${t.foreground}" opacity="0.85"/>
	<circle cx="454" cy="432" r="14" fill="none" stroke="${t.border}" stroke-width="1.5"/>
	<rect x="478" y="422" width="140" height="9" rx="4.5" fill="${t.foreground}" opacity="0.6"/>
	<rect x="478" y="437" width="90" height="7" rx="3.5" fill="${t.muted}" opacity="0.4"/>

	<!-- The summary card: sparkle, title, three bullets. -->
	<rect x="440" y="470" width="600" height="132" rx="12" fill="${t.sidebar}" stroke="${t.border}"/>
	<path d="M463 488 l2.6 6.4 l6.4 2.6 l-6.4 2.6 l-2.6 6.4 l-2.6 -6.4 l-6.4 -2.6 l6.4 -2.6 z" fill="${t.foreground}"/>
	<rect x="480" y="492" width="44" height="10" rx="5" fill="${t.foreground}" opacity="0.8"/>
	${[0, 1, 2]
		.map(
			(i) =>
				`<circle cx="462" cy="${530 + i * 24}" r="2.5" fill="${t.foreground}" opacity="0.6"/><rect x="474" y="${526 + i * 24}" width="${[380, 440, 320][i]}" height="8" rx="4" fill="${t.foreground}" opacity="0.4"/>`,
		)
		.join("")}
	${[0, 1].map((i) => `<rect x="440" y="${626 + i * 22}" width="${[560, 480][i]}" height="8" rx="4" fill="${t.muted}" opacity="0.3"/>`).join("")}
</svg>`;
}

/** Each language's image at its path's name, as /og/zh-hans.png. */
export const getStaticPaths = (() => locales.map((lang) => ({ params: { lang: paths[lang].slice(1, -1) }, props: { lang } }))) satisfies GetStaticPaths;

export const GET: APIRoute<{ lang: Lang }> = ({ props }) => {
	const png = new Resvg(svg(props.lang), { font: { fontFiles: fonts, loadSystemFonts: false, defaultFontFamily: "Geist" } }).render().asPng();
	return new Response(png, { headers: { "Content-Type": "image/png" } });
};
