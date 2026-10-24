// The About page's bird: the site's paper godwit (site/public/godwit.glb, flown by site/src/bird.ts)
// in the logo's side view; while the pointer is over it, it flies in place in the site's rhythm of
// three beats and a glide. A drag turns it; let go, it turns back. When the reader prefers reduced motion it holds the logo's pose.
import {
  ACESFilmicToneMapping,
  DirectionalLight,
  Group,
  Mesh,
  MeshStandardMaterial,
  PerspectiveCamera,
  PMREMGenerator,
  Scene,
  SRGBColorSpace,
  Vector3,
  WebGLRenderer,
} from "three";
import { RoomEnvironment } from "three/addons/environments/RoomEnvironment.js";
import { MeshoptDecoder } from "three/addons/libs/meshopt_decoder.module.js";
import { GLTFLoader } from "three/addons/loaders/GLTFLoader.js";
import model from "../../../site/public/godwit.glb?url";

const clamp = (q: number, lo = 0, hi = 1) => Math.min(Math.max(q, lo), hi);
const ease = (q: number) => 0.5 - 0.5 * Math.cos(Math.PI * clamp(q));
const wave = (t: number, period: number, phase = 0) => Math.sin((2 * Math.PI * t) / period + phase);
const beat = (p: number, stroke: number) =>
  p < stroke ? ease(p / stroke) : 1 - ease((p - stroke) / (1 - stroke));

type Pose = { down: number; y: number; roll: number; yaw: number; pitch: number };
// bird.ts's glide: three beats, then a glide on level wings, sinking a little
function glide(t: number): Pose {
  const period = 0.75,
    beats = 3,
    glide = 2,
    flapping = period * beats,
    c = t % (flapping + glide);
  if (c < flapping) {
    const p = (c / period) % 1;
    return {
      down: beat(p, 0.4),
      y: -0.02 + 0.04 * ease(c / flapping) + 0.008 * Math.sin(2 * Math.PI * (p - 0.15)),
      roll: 0.05 * wave(t, 8),
      yaw: 0.04 * wave(t, 11, 1),
      pitch: 0.03,
    };
  }
  const g = (c - flapping) / glide,
    level = 0.65;
  const down =
    g < 0.25
      ? level * ease(g / 0.25)
      : g > 0.85
        ? level * (1 - ease((g - 0.85) / 0.15))
        : level + 0.04 * Math.sin(2 * Math.PI * (g - 0.25) * 1.5);
  return {
    down,
    y: 0.02 - 0.04 * ease(g),
    roll: 0.05 * wave(t, 8),
    yaw: 0.04 * wave(t, 11, 1),
    pitch: 0.03 - 0.07 * Math.sin(Math.PI * g),
  };
}
const pitch = 0.1,
  bottom = (45 * Math.PI) / 180;
// bird.ts's hinges: each wing turns about the line where its root meets the back
const hinges = {
  WingL: { at: [-0.1248, 0.1405, -0.0845], axis: [-0.5935, -0.6462, -0.4799] },
  WingR: { at: [-0.1198, 0.1479, 0.0792], axis: [0.5678, 0.6633, -0.4874] },
} as const;

async function mount(canvas: HTMLCanvasElement) {
  let renderer: WebGLRenderer;
  try {
    renderer = new WebGLRenderer({ canvas, antialias: true, alpha: true });
  } catch {
    return; // no WebGL: no bird
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

  const gltf = await new GLTFLoader().setMeshoptDecoder(MeshoptDecoder).loadAsync(model);
  const turn = new Group(),
    bird = new Group();
  gltf.scene.position.y = -0.21; // the body's middle at the origin
  bird.add(gltf.scene);
  turn.add(bird);
  turn.scale.setScalar(0.8);
  scene.add(turn);
  gltf.scene.traverse((o) => {
    if (o instanceof Mesh && o.material instanceof MeshStandardMaterial) o.material.side = 2; // thin paper: both sides show
  });
  const wings = (Object.keys(hinges) as (keyof typeof hinges)[]).map((name) => {
    // biome-ignore lint/style/noNonNullAssertion: the model has both wings
    const wing = gltf.scene.getObjectByName(name)!;
    const pivot = new Group();
    pivot.position.fromArray(hinges[name].at);
    wing.parent?.add(pivot);
    pivot.attach(wing);
    return { pivot, axis: new Vector3().fromArray(hinges[name].axis) };
  });

  const camera = new PerspectiveCamera(24, 1, 0.1, 20);
  camera.position.set(0.45, 0.17, 1.95);
  camera.lookAt(0, 0, 0);
  const draw = (m: Pose) => {
    for (const w of wings) w.pivot.quaternion.setFromAxisAngle(w.axis, m.down * bottom);
    bird.position.y = m.y;
    bird.rotation.set(m.roll, m.yaw, pitch + m.pitch);
    renderer.render(scene, camera);
  };

  // A drag turns the bird by its width in half turns; let go, it eases back.
  let spin = 0,
    dragFrom: number | null = null,
    dragSpin = 0;
  canvas.addEventListener("pointerdown", (e) => {
    canvas.setPointerCapture(e.pointerId);
    dragFrom = e.clientX - spin * (innerWidth / Math.PI);
  });
  canvas.addEventListener("pointermove", (e) => {
    if (dragFrom !== null) dragSpin = ((e.clientX - dragFrom) / innerWidth) * Math.PI;
  });
  const release = () => {
    dragFrom = null;
    dragSpin = 0;
  };
  canvas.addEventListener("pointerup", release);
  canvas.addEventListener("pointercancel", release);

  // It flies only while the pointer is over it, easing into the rhythm and back to the logo's
  // pose; settled, the loop stops.
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  let hover = false,
    fly = 0,
    t = 0,
    last = 0;
  const pose = (): Pose => {
    const g = glide(t);
    return {
      down: g.down * fly,
      y: g.y * fly,
      roll: g.roll * fly,
      yaw: g.yaw * fly,
      pitch: g.pitch * fly,
    };
  };
  const frame = (now: number) => {
    const dt = last ? (now - last) / 1000 : 0;
    last = now;
    const dragging = dragFrom !== null;
    spin += ((dragging ? dragSpin : 0) - spin) * clamp(dt * (dragging ? 20 : 4));
    fly = clamp(fly + (hover && !reduced ? dt : -dt) * 2);
    if (fly > 0) t += dt;
    turn.rotation.y = spin;
    draw(pose());
    if (!hover && !dragging && fly === 0 && Math.abs(spin) < 1e-3) {
      renderer.setAnimationLoop(null);
      t = last = 0;
    }
  };
  const wake = () => renderer.setAnimationLoop(frame);
  canvas.addEventListener("pointerenter", () => {
    hover = true;
    wake();
  });
  canvas.addEventListener("pointerleave", () => {
    hover = false;
  });
  canvas.addEventListener("pointerdown", wake);
  const resize = () => {
    renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
    renderer.setSize(innerWidth, innerHeight, false);
    camera.aspect = innerWidth / innerHeight;
    camera.updateProjectionMatrix();
    draw(pose());
  };
  addEventListener("resize", resize);
  resize();
}

// biome-ignore lint/style/noNonNullAssertion: about.html always has #bird
mount(document.getElementById("bird")! as HTMLCanvasElement);
