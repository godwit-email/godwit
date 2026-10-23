// The hero's bird: the logo's godwit folded from paper (public/godwit.glb: made
// with Tripo from origami views of the 3D model and the logo, generated as separate
// body and wing parts; its sources and build are in godwit-email/brand), drawn live
// with three.js on a canvas over the whole window.
// It flies in from the bottom left, close to the reader and away into the page, settles beside
// the headline, circling there in a rhythm of beats and glides, always facing the
// way it flies, and as the page scrolls it dives down to the app window. Each wing is a
// rigid flap hinged along the line where its root meets the back. When the reader
// prefers reduced motion it stays at its perch in the logo's pose.
import { ACESFilmicToneMapping, DirectionalLight, Group, Mesh, MeshStandardMaterial, PerspectiveCamera, PMREMGenerator, Scene, SRGBColorSpace, Vector3, WebGLRenderer } from "three";
import { RoomEnvironment } from "three/addons/environments/RoomEnvironment.js";
import { MeshoptDecoder } from "three/addons/libs/meshopt_decoder.module.js";
import { GLTFLoader } from "three/addons/loaders/GLTFLoader.js";

const clamp = (q: number, lo = 0, hi = 1) => Math.min(Math.max(q, lo), hi);
const ease = (q: number) => 0.5 - 0.5 * Math.cos(Math.PI * clamp(q));
const wave = (t: number, period: number, phase = 0) => Math.sin((2 * Math.PI * t) / period + phase);

/** One wingbeat at phase p (0 at the top): how far down the wings are (0 the logo's pose,
 * 1 the bottom), the downstroke taking `stroke` of the beat. */
function beat(p: number, stroke: number): number {
	return p < stroke ? ease(p / stroke) : 1 - ease((p - stroke) / (1 - stroke));
}

/** The wings (0 to 1 down) and the body's bob and turn at time t. */
type Pose = { down: number; y: number; roll: number; yaw: number; pitch: number };
// quick, full wingbeats: the flight in
function cruise(t: number): Pose {
	const p = (t / 0.5) % 1;
	return { down: beat(p, 0.42), y: 0.018 * Math.sin(2 * Math.PI * (p - 0.2)), roll: 0.07 * wave(t, 9), yaw: 0.05 * wave(t, 13, 1), pitch: 0.03 * wave(t, 6.8, 0.5) };
}
// at the perch: three beats, then a glide on level wings, sinking a little, then beating again
function glide(t: number): Pose {
	const period = 0.75, beats = 3, glide = 2, flapping = period * beats, c = t % (flapping + glide);
	if (c < flapping) {
		const p = (c / period) % 1;
		return { down: beat(p, 0.4), y: -0.02 + 0.04 * ease(c / flapping) + 0.008 * Math.sin(2 * Math.PI * (p - 0.15)), roll: 0.05 * wave(t, 8), yaw: 0.04 * wave(t, 11, 1), pitch: 0.03 };
	}
	const g = (c - flapping) / glide, level = 0.65;
	// from the top of the last beat down to level, a slow sway, and back up for the next
	const down = g < 0.25 ? level * ease(g / 0.25) : g > 0.85 ? level * (1 - ease((g - 0.85) / 0.15)) : level + 0.04 * Math.sin(2 * Math.PI * (g - 0.25) * 1.5);
	return { down, y: 0.02 - 0.04 * ease(g), roll: 0.05 * wave(t, 8), yaw: 0.04 * wave(t, 11, 1), pitch: 0.03 - 0.07 * Math.sin(Math.PI * g) };
}
const intro = 2; // the flight in: four quick beats, ending wings up where the glide rhythm starts
const pitch = 0.1; // nose up a little, as in the logo
const bottom = (45 * Math.PI) / 180; // the downstroke: 45° below the logo's raised wings
const fov = 24;
// Each wing's hinge, as godwit-email/brand's origami/build.sh prints it: a point on the line where its root meets the back, and the
// direction of that line, signed so a positive turn lowers the wing. Turning about the root's own
// line keeps the root on the body; a level axis would lift its ends off and show the cut.
const hinges = {
	WingL: { at: [-0.1248, 0.1405, -0.0845], axis: [-0.5935, -0.6462, -0.4799] },
	WingR: { at: [-0.1198, 0.1479, 0.0792], axis: [0.5678, 0.6633, -0.4874] },
} as const;

/** Flies the bird on `canvas` (fixed over the window): it circles beside `headline` (right of
 * it where there is room, else over `perch`, an empty spot above it) without covering it, and the
 * page's scroll takes it down to `land`, the app window. */
export async function mount(canvas: HTMLCanvasElement, perch: HTMLElement, headline: HTMLElement, land: HTMLElement | null): Promise<void> {
	let renderer: WebGLRenderer;
	try {
		renderer = new WebGLRenderer({ canvas, antialias: true, alpha: true });
	} catch {
		return; // no WebGL: the hero goes without its bird
	}
	renderer.outputColorSpace = SRGBColorSpace;
	renderer.toneMapping = ACESFilmicToneMapping;
	const scene = new Scene();
	scene.environment = new PMREMGenerator(renderer).fromScene(new RoomEnvironment(), 0.04).texture;
	scene.environmentIntensity = 0.45;
	const key = new DirectionalLight(0xffffff, 2);
	key.position.set(1.5, 2.5, 2);
	const rim = new DirectionalLight(0xfff4e6, 2.5);
	rim.position.set(-2, 1, -2.5);
	scene.add(key, rim);

	const gltf = await new GLTFLoader().setMeshoptDecoder(MeshoptDecoder).loadAsync("/godwit.glb");
	const flight = new Group(); // where the bird is on the page, its size, and its dive or climb
	const bird = new Group(); // its bob and bank
	gltf.scene.position.y = -0.21; // the body's middle at the origin
	bird.add(gltf.scene);
	flight.add(bird);
	scene.add(flight);
	gltf.scene.traverse((o) => {
		if (o instanceof Mesh && o.material instanceof MeshStandardMaterial) o.material.side = 2; // thin paper: both sides show
	});
	// The hinges are kept here: compressing the file moves the wings' own origins.
	const wings = (Object.keys(hinges) as (keyof typeof hinges)[]).map((name) => {
		const wing = gltf.scene.getObjectByName(name)!;
		const pivot = new Group();
		pivot.position.fromArray(hinges[name].at);
		wing.parent!.add(pivot);
		pivot.attach(wing);
		return { pivot, axis: new Vector3().fromArray(hinges[name].axis) };
	});

	// Nearly the logo's side view, a little ahead and above so the far wing shows.
	const camera = new PerspectiveCamera(fov, 1, 0.1, 20);
	camera.position.set(0.45, 0.17, 1.95);
	camera.lookAt(0, 0, 0);
	const facing = camera.getWorldDirection(new Vector3());
	const resize = () => {
		renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
		renderer.setSize(innerWidth, innerHeight, false);
		camera.aspect = innerWidth / innerHeight;
		camera.updateProjectionMatrix();
	};
	addEventListener("resize", resize);
	resize();

	/** The point on the plane through the origin facing the camera that shows at window pixel (x, y). */
	const toWorld = (x: number, y: number, out: Vector3) => {
		out.set((x / innerWidth) * 2 - 1, 1 - (y / innerHeight) * 2, 0.5).unproject(camera).sub(camera.position);
		return out.multiplyScalar(-camera.position.dot(facing) / out.dot(facing)).add(camera.position);
	};
	const worldPerPixel = () => (2 * camera.position.length() * Math.tan((fov * Math.PI) / 360)) / innerHeight;
	// the bird's length at its perch, in pixels: by the window's width, but small enough to circle
	// in the room above the headline (from its top on the page, so steady while scrolling)
	const length = () => Math.min(clamp(innerWidth * 0.3, 190, 360), Math.max(120, (headline.getBoundingClientRect().top + scrollY) / 0.85));
	const loop = () => length() * (innerWidth >= 900 ? 0.45 : 0.25); // how far the loop swings either side
	// Window pixels: the middle of the loop, and the app window's top right corner.
	const perchAt = () => {
		// the headline's text, not its full-width box
		const lines = [...headline.children].map((c) => c.getBoundingClientRect());
		const right = Math.max(...lines.map((r) => r.right)), top = Math.min(...lines.map((r) => r.top)), bottom = Math.max(...lines.map((r) => r.bottom));
		// half the bird at the front of the loop, where it is nearest so largest, past the swing
		const reach = loop() + length() * 0.65;
		if (right + 2 * reach <= innerWidth - 16) return { x: right + reach, y: (top + bottom) / 2 };
		const r = perch.getBoundingClientRect();
		const x = Math.min(Math.max(r.left + r.width * (innerWidth >= 640 ? 0.75 : 0.6), reach), innerWidth - reach);
		return { x, y: top - length() * 0.4 };
	};
	const landAt = () => {
		const r = land!.getBoundingClientRect();
		return { x: r.left + r.width * 0.86, y: r.top - length() * 0.1 };
	};
	/** How far the page has scrolled the bird from its perch to the app window, 0 to 1: it arrives
	 * as the window's top reaches a quarter of the way down the screen. */
	const travel = () => (land ? ease(scrollY / Math.max(1, land.getBoundingClientRect().top + scrollY - innerHeight * 0.25)) : 0);

	let last = { y: 0, yaw: 0, t: -1 }, vy = 0, turn = 0, shown = true;
	/** Draws the bird at time t (seconds since load), the wings and body as `m`. Besides where it
	 * shows on the page, it has a depth (`far`: 1 at the page, more is further back along the line
	 * of sight, so smaller) and a heading (`yaw`: 0 the logo's side view, less turns it toward the
	 * reader, more away). */
	/** Draws the bird; false once it is off screen and drawn empty. `still` keeps it at its perch
	 * rather than diving with the page's scroll. */
	const draw = (t: number, m: Pose, still = false): boolean => {
		const p = perchAt(), s = still ? 0 : travel(), size = length() * (1 - 0.25 * s);
		let { x, y } = p, far = 1, yaw = 0;
		// A bird can't hang in the air: at the perch it circles, on a flat loop around it (nearer
		// the reader at the front, further at the back), always heading where it is going.
		const A = loop(), B = 0.22, period = 11;
		if (t < intro) {
			// a curve from the bottom left, close to the reader (large): up and away into the page
			// first, then swinging right to meet the front of the loop level, at about its speed.
			// Points are window pixels and depth (`far`).
			const u = t / intro, q = u * (1.4 - 0.4 * u);
			const P = [
				[innerWidth * 0.02, innerHeight * 0.95, 0.3],
				[innerWidth * 0.06, innerHeight * 0.5, 0.95],
				[p.x - innerWidth * 0.12, p.y, 1 - B],
				[p.x, p.y, 1 - B],
			];
			const r = 1 - q, w = [r ** 3, 3 * r * r * q, 3 * r * q * q, q ** 3], dw = [-3 * r * r, 3 * r * (r - 2 * q), 3 * q * (2 * r - q), 3 * q * q];
			const at = (i: number, k: number[]) => k.reduce((sum, c, j) => sum + c * P[j][i], 0);
			x = at(0, w);
			y = at(1, w);
			far = at(2, w);
			// heading along the curve: across the page in pixels, into it by the camera's distance
			yaw = Math.atan2(at(2, dw) * camera.position.length(), at(0, dw) * worldPerPixel());
			// each downstroke lifts the body a little
			y -= length() * 0.05 * Math.sin(2 * Math.PI * (t / 0.5 - 0.15)) * (1 - u);
		} else {
			const a = -Math.PI / 2 + (2 * Math.PI * (t - intro)) / period; // the front of the loop at the start
			x += A * Math.cos(a);
			y += length() * 0.04 * Math.sin(2 * a);
			far = 1 + B * Math.sin(a);
			// heading along the loop: across the page by A, into it by B of the camera's distance
			yaw = Math.atan2(B * camera.position.length() * Math.cos(a), A * worldPerPixel() * -Math.sin(a));
		}
		if (land) {
			const l = landAt();
			// leaving the loop for the app window: bowing out to the right on the way, around the
			// headline rather than across it, and turned away from the reader through the dive
			const k = ease(s * 2);
			x += (l.x - x) * s + Math.sin(Math.PI * s) * Math.min(innerWidth * 0.15, 220);
			y += (l.y - y) * s;
			far += (1 - far) * k;
			const to = 0.7 * Math.sin(Math.PI * s), d = Math.atan2(Math.sin(to - yaw), Math.cos(to - yaw));
			yaw += d * k;
		}
		// the heading turns continuously: keep it within half a turn of the last one
		yaw += 2 * Math.PI * Math.round((last.yaw - yaw) / (2 * Math.PI));
		// Off screen: drawn once empty, then left alone.
		const off = y < -size || y > innerHeight + size || x > innerWidth + size;
		if (off && !shown) return false;
		flight.visible = shown = !off;
		// dive while moving down the page, climb while moving up, and bank into turns: from the
		// smoothed speeds
		if (last.t >= 0 && t > last.t) {
			const dt = t - last.t, k = clamp(dt * 6);
			vy += ((y - last.y) / dt - vy) * k;
			turn += ((yaw - last.yaw) / dt - turn) * k;
		}
		last = { y, yaw, t };
		toWorld(x, y, flight.position).sub(camera.position).multiplyScalar(far).add(camera.position);
		flight.scale.setScalar(size * worldPerPixel());
		flight.rotation.set(clamp(-0.5 * turn, -0.6, 0.6), yaw, clamp(-vy / (size * 4), -0.45, 0.35), "YXZ");
		for (const w of wings) w.pivot.quaternion.setFromAxisAngle(w.axis, m.down * bottom);
		bird.position.y = m.y;
		bird.rotation.set(m.roll, m.yaw, pitch + m.pitch);
		renderer.render(scene, camera);
		return !off;
	};

	if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
		// at its perch, wings up as in the logo, moving only with the page
		const still = () => draw(intro, { down: 0, y: 0, roll: 0, yaw: 0, pitch: 0 }, true);
		addEventListener("scroll", still, { passive: true });
		addEventListener("resize", still);
		still();
		return;
	}
	// The browser pauses the frames of a hidden tab; off screen, the loop stops until a scroll
	// or resize could bring the bird back.
	const start = performance.now();
	const frame = (now: number) => {
		const t = (now - start) / 1000;
		if (draw(t, t < intro ? cruise(t) : glide(t - intro))) return;
		renderer.setAnimationLoop(null);
		addEventListener("scroll", resume, { passive: true });
		addEventListener("resize", resume);
	};
	const resume = () => {
		removeEventListener("scroll", resume);
		removeEventListener("resize", resume);
		renderer.setAnimationLoop(frame);
	};
	renderer.setAnimationLoop(frame);
}
