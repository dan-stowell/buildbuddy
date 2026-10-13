package main

import (
	"reflect"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/util/shlex"
	"github.com/mattn/go-shellwords"
)

func TestParserComparison(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		different   bool
		wantErr     bool
	}{
		{"plain", "echo one two", false, false},
		{"quotes", `echo 'one two' "three four"`, false, false},
		{"adjacent_quotes", `echo a'b c'"d"`, false, false},
		{"escaped_space", `echo one\ two`, false, false},
		{"escaped_quote", `echo "a\"b"`, false, false},
		{"double_quote_escape", `echo "a\qb"`, false, false},
		{"empty_args", `echo '' "" end`, false, false},
		{"empty_command", "", false, false},
		{"unclosed_single", `echo 'bad`, false, true},
		{"unclosed_double", `echo "bad`, false, true},
		{"trailing_escape", `echo bad\`, false, true},
		{"semicolon", `echo first; echo second`, true, false},
		{"comment", `echo first # comment`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ae := shellwords.Parse(tc.input)
			b, be := shlex.Split(tc.input)
			if (ae != nil) != tc.wantErr || (be != nil) != tc.wantErr {
				t.Fatalf("shellwords error=%v, shlex error=%v; want error=%v", ae, be, tc.wantErr)
			}
			different := (ae == nil) != (be == nil) || (ae == nil && be == nil && !reflect.DeepEqual(a, b))
			t.Logf("shellwords=%q err=%v; shlex=%q err=%v", a, ae, b, be)
			if different != tc.different {
				t.Errorf("difference=%v, expected %v", different, tc.different)
			}
		})
	}
}
