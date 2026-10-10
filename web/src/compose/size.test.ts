import { byteSize } from "./size";

test("byteSize", () => {
  expect(byteSize(512)).toBe("512 B");
  expect(byteSize(1536)).toBe("2 KB");
  expect(byteSize(26214400)).toBe("25.0 MB");
});
