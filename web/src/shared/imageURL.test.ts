import { afterEach, expect, test } from "vitest";
import { installFake } from "@/preview/fake";
import { imageURL } from "./imageURL";

afterEach(() => {
  delete window.mygo;
});

test("the image proxy's custom scheme where the webview loads it", () => {
  expect(imageURL("a42")).toBe("godwit-img://a42");
  installFake({}, "darwin");
  expect(imageURL("a42")).toBe("godwit-img://a42");
  expect(imageURL("m7", "/tok")).toBe("godwit-img://m7/tok");
});

test("on Windows MyGo's http://<host>.<scheme>.localhost form: WebView2 requests no custom scheme (#750)", () => {
  installFake({}, "win32");
  expect(imageURL("a42")).toBe("http://a42.godwit-img.localhost/");
  expect(imageURL("m7", "/tok")).toBe("http://m7.godwit-img.localhost/tok");
});
