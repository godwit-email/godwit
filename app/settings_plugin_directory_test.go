package app

import (
	"reflect"
	"testing"
)

func TestParseReadme(t *testing.T) {
	md := "# Zen Free\n\n[English](README.md) | [简体中文](README.zh-Hans.md)\n\n![badge](https://x/y.svg)\n\nOne **bold** line\ncontinues with `code` and a [link](https://x).\n\n## Use\n\n1. First\n- Second <b>tag</b>\n\n```sh\nrun # kept verbatim\n```\n"
	want := []readmeBlock{
		{kind: "p", text: "One bold line continues with code and a link."},
		{kind: "h", text: "Use"},
		{kind: "li", mark: "1.", text: "First"},
		{kind: "li", mark: "•", text: "Second tag"},
		{kind: "code", text: "run # kept verbatim"},
	}
	if got := parseReadme(md); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}
