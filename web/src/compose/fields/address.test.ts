import { commit, invalid } from "./address";

test("commit splits, trims and skips empty and duplicates", () => {
  expect(commit("a@b.c, a@b.c,  ,c@d.e", [], false)).toEqual(["a@b.c", "c@d.e"]);
  expect(commit("a@b.c,c@d.e", ["a@b.c"], false)).toEqual(["a@b.c", "c@d.e"]);
  expect(commit("x@y.z,w@v.u", ["a@b.c"], true)).toEqual(["w@v.u"]);
  expect(commit(" , ", ["a@b.c"], true)).toEqual(["a@b.c"]);
});

test("invalid", () => {
  expect(invalid(["ok@x.io", "Name <n@x.io>", "bad"])).toEqual(["bad"]);
  expect(
    invalid([
      '"Doe, J." <j@x.io>',
      "<a@b>",
      "张三 <z@x.cn>",
      "a.b+c@x.io",
      "a@",
      "@x.io",
      "a b@x.io",
      "a..b@x.io",
      "Name <bad>",
      "a@b.c>",
    ]),
  ).toEqual(["a@", "@x.io", "a b@x.io", "a..b@x.io", "Name <bad>", "a@b.c>"]);
});

test("commas inside a quoted name do not split", () => {
  expect(commit('"Doe, J." <j@example.com>, a@b.c', [], false)).toEqual([
    '"Doe, J." <j@example.com>',
    "a@b.c",
  ]);
  expect(commit('"a\\", b" <x@y.z>', [], false)).toEqual(['"a\\", b" <x@y.z>']);
});

test("quoted local parts are addresses", () => {
  expect(invalid(['"john doe"@x.io', 'Name <"a,b"@x.io>', '"open@x.io'])).toEqual(['"open@x.io']);
});
