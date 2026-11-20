package pyutf8

import (
	"testing"
)

func TestDecodeReplace(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{
			name: "valid ascii",
			in:   []byte("hello world"),
			want: "hello world",
		},
		{
			name: "valid utf8",
			in:   []byte("hello ñandú 😀"),
			want: "hello ñandú 😀",
		},
		{
			name: "prompt case 1",
			in:   []byte("\xe2\x80x\xff\xfe\xc3(\xf0\x9f\x98"),
			want: "\uFFFDx\uFFFD\uFFFD\uFFFD(\uFFFD",
		},
		{
			name: "prompt case 2",
			in:   []byte("ok\xff\xfeend\xc3(x\n"),
			want: "ok\uFFFD\uFFFDend\uFFFD(x\n",
		},
		{
			name: "surrogate ed a0 80",
			in:   []byte("\xed\xa0\x80"),
			want: "\uFFFD\uFFFD\uFFFD",
		},
		{
			name: "overlong c0 80",
			in:   []byte("\xc0\x80"),
			want: "\uFFFD\uFFFD",
		},
		{
			name: "overlong e0 80 80",
			in:   []byte("\xe0\x80\x80"),
			want: "\uFFFD\uFFFD\uFFFD",
		},
		{
			name: "overlong f0 80 80 80",
			in:   []byte("\xf0\x80\x80\x80"),
			want: "\uFFFD\uFFFD\uFFFD\uFFFD",
		},
		{
			name: "out of range f4 90 80 80",
			in:   []byte("\xf4\x90\x80\x80"),
			want: "\uFFFD\uFFFD\uFFFD\uFFFD",
		},
		{
			name: "partial 4-byte 3-byte prefix",
			in:   []byte("\xf0\x90\x80x"),
			want: "\uFFFDx",
		},
		{
			name: "partial 4-byte 2-byte prefix",
			in:   []byte("\xf0\x90xx"),
			want: "\uFFFDxx",
		},
		{
			name: "orphan continuation",
			in:   []byte("\x80\xbf"),
			want: "\uFFFD\uFFFD",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecodeReplace(tc.in)
			if got != tc.want {
				t.Fatalf("DecodeReplace(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
