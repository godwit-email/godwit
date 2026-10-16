import { expect, test } from "bun:test";

const config = new URL("../mygo.config.ts", import.meta.url).href;
const load = (profile = "", entitlements = "") =>
  Bun.spawnSync({
    cmd: [process.execPath, "-e", `import c from ${JSON.stringify(config)}; console.log(JSON.stringify(c.macos))`],
    env: { ...process.env, MACOS_PROVISIONING_PROFILE: profile, MACOS_ENTITLEMENTS: entitlements },
  });

test("default builds leave out the restricted entitlement and profile", () => {
  const result = load();
  expect(result.exitCode).toBe(0);
  const macos = JSON.parse(result.stdout.toString());
  expect(macos.provisioningProfile).toBeFalsy();
  expect(macos.entitlements).toBe("assets/entitlements.plist");
});

test("signed builds pass both supplied files to MyGo", () => {
  const result = load("/tmp/Godwit.provisionprofile", "/tmp/Godwit.entitlements.plist");
  expect(result.exitCode).toBe(0);
  const macos = JSON.parse(result.stdout.toString());
  expect(macos.provisioningProfile).toBe("/tmp/Godwit.provisionprofile");
  expect(macos.entitlements).toBe("/tmp/Godwit.entitlements.plist");
});

test("an incomplete signing configuration fails before building", () => {
  for (const [profile, entitlements] of [["/tmp/profile", ""], ["", "/tmp/entitlements"]]) {
    const result = load(profile, entitlements);
    expect(result.exitCode).not.toBe(0);
    expect(result.stderr.toString()).toContain("Set both MACOS_PROVISIONING_PROFILE and MACOS_ENTITLEMENTS");
  }
});
