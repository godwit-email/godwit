import { expect, test } from "vitest";
import { listTime } from "./Citations";

test("older days read M/DD", () => {
  const now = new Date(2026, 9, 11, 9, 0); // Sunday
  const t = ((k: string) => k) as never;
  expect(listTime(now, new Date(2026, 9, 3, 9).getTime(), t)).toBe("10/03");
  expect(listTime(now, new Date(2026, 8, 28, 9).getTime(), t)).toBe("9/28");
});
