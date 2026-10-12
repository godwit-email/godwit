// The page's motion, with Motion's DOM API. The hero comes in on load, the
// demo window tips up into place as it scrolls in, and sections rise as they
// enter. Elements start hidden only under html.motion, which the head sets
// unless the reader prefers reduced motion (and styles.css reveals them anyway
// after a while, should this script not run).
import { animate, inView, press, scroll, stagger } from "motion";

/** The page's spring: settles quickly, without a visible bounce. */
export const spring = { type: "spring", stiffness: 260, damping: 32, mass: 1 } as const;

if (document.documentElement.classList.contains("motion")) {
	document.documentElement.classList.add("motion-ready"); // styles.css: no fallback reveal
	const hero = document.querySelectorAll<HTMLElement>("[data-hero] > *");
	animate(hero, { opacity: [0, 1], y: [16, 0], filter: ["blur(6px)", "blur(0px)"] }, { ...spring, delay: stagger(0.07) });

	const demo = document.querySelector<HTMLElement>("[data-demo]");
	if (demo) {
		animate(demo, { opacity: [0, 1] }, { duration: 0.6, delay: 0.35 }); // the tilt below owns its transform
		// Tips up from a slight tilt as it scrolls to the upper part of the
		// screen; once flat it has no transform, so its text renders crisp.
		const from = { rotate: 10, scale: 0.94 };
		scroll(
			(progress: number) => {
				const t = Math.min(Math.max(progress, 0), 1);
				demo.style.transform = t === 1 ? "" : `perspective(1600px) rotateX(${from.rotate * (1 - t)}deg) scale(${from.scale + (1 - from.scale) * t})`;
			},
			{ target: demo, offset: ["start end", "start 0.25"] },
		);
	}

	inView(
		"[data-reveal]",
		(el) => {
			animate(el, { opacity: [0, 1], y: [24, 0] }, spring);
		},
		{ amount: 0.3 },
	);
	inView(
		"[data-reveal-group]",
		(el) => {
			// An array: Motion takes an HTMLCollection for one plain object.
			animate([...el.children], { opacity: [0, 1], y: [20, 0] }, { ...spring, delay: stagger(0.06) });
		},
		{ amount: 0.2 },
	);
}

// Buttons marked data-press give a little under the pointer.
press("[data-press]", (el) => {
	animate(el, { scale: 0.97 }, { duration: 0.12 });
	return () => animate(el, { scale: 1 }, spring);
});
