package main

import "testing"

func TestApplyMentions(t *testing.T) {
	// Keys are lowercased, mirroring Viper's GetStringMapString.
	mentions := map[string]string{
		"amplía soluciones": "urn:li:organization:123",
		"antonio gulli":     "urn:li:person:abcd",
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "known mention expands, case-insensitive",
			in:   "Built by {{@Amplía Soluciones}} today.",
			want: "Built by @[Amplía Soluciones](urn:li:organization:123) today.",
		},
		{
			name: "two mentions",
			in:   "{{@Antonio Gulli}} and {{@Amplía Soluciones}}",
			want: "@[Antonio Gulli](urn:li:person:abcd) and @[Amplía Soluciones](urn:li:organization:123)",
		},
		{
			name: "unknown mention falls back to plain name",
			in:   "thanks {{@Nobody Known}}!",
			want: "thanks Nobody Known!",
		},
		{
			name: "no tokens passes through unchanged",
			in:   "plain text, no mentions",
			want: "plain text, no mentions",
		},
		{
			name: "raw mention syntax is left untouched",
			in:   "@[Name](urn:li:person:x)",
			want: "@[Name](urn:li:person:x)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applyMentions(c.in, mentions); got != c.want {
				t.Errorf("applyMentions(%q)\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}

func TestCommentaryUTF16Len(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"𝗥", 2},   // SMP bold glyph = 2 UTF-16 code units
		{"a𝗥b", 4}, // 1 + 2 + 1
	}
	for _, c := range cases {
		if got := commentaryUTF16Len(c.in); got != c.want {
			t.Errorf("commentaryUTF16Len(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
