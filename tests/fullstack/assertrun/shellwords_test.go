package main

import (
	"reflect"
	"testing"
)

func TestSplitShellWords(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{
			name: "plain probe command prefix",
			in:   "compose exec -T clickhouse clickhouse-client --user default --password ch --database acr_e2e --query",
			want: []string{"compose", "exec", "-T", "clickhouse", "clickhouse-client", "--user", "default", "--password", "ch", "--database", "acr_e2e", "--query"},
		},
		{
			name: "trailing whitespace from printf %q formatting",
			in:   "compose exec -T clickhouse ",
			want: []string{"compose", "exec", "-T", "clickhouse"},
		},
		{
			name: "single quoted word with spaces",
			in:   `foo 'bar baz' qux`,
			want: []string{"foo", "bar baz", "qux"},
		},
		{
			name: "double quoted word with escaped dollar and quote",
			in:   `"hello \"there\" \$5"`,
			want: []string{`hello "there" $5`},
		},
		{
			name: "adjacent quotes form one word",
			in:   `foo'bar'"baz"`,
			want: []string{"foobarbaz"},
		},
		{
			name: "ansi-c quoting decodes escapes",
			in:   `$'a\tb\nc'`,
			want: []string{"a\tb\nc"},
		},
		{
			name: "backslash escapes next unquoted character",
			in:   `foo\ bar`,
			want: []string{"foo bar"},
		},
		{
			name: "empty input yields no words",
			in:   "   ",
			want: nil,
		},
		{
			name:    "unterminated single quote is an error",
			in:      `foo 'bar`,
			wantErr: true,
		},
		{
			name:    "unterminated double quote is an error",
			in:      `"foo`,
			wantErr: true,
		},
		{
			name:    "dangling backslash is an error",
			in:      `foo\`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitShellWords(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitShellWords(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitShellWords(%q) unexpected error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitShellWords(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}
