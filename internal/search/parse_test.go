package search

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	w := func(s string) Term { return Term{Text: s} }
	p := func(s string) Term { return Term{Text: s, Prefix: true} }
	ph := func(s string) Term { return Term{Text: s, Phrase: true} }
	tests := []struct {
		in   string
		want []Term
	}{
		{"rate limit", []Term{w("rate"), w("limit")}},
		{"e159*", []Term{p("e159")}},
		{"e159**", []Term{p("e159")}},
		{"commit:e159e8c", []Term{w("commit:e159e8c")}},
		{`"rate limit" hit`, []Term{ph("rate limit"), w("hit")}},
		{`"rate limit"*`, []Term{ph("rate limit")}},
		{`"rate   limit"`, []Term{ph("rate limit")}},
		{"** !!! a", []Term{w("a")}},
		{`""`, nil},
		{`"  "`, nil},
		{`"!!!"`, nil},
		{"", nil},
		{`"unterminated phrase`, []Term{ph("unterminated phrase")}},
		{"Hello", []Term{w("Hello")}},
		{"it's", []Term{w("it's")}},
		{`say"hi`, []Term{w(`say"hi`)}},
		{"café*", []Term{p("café")}},
	}
	for _, tc := range tests {
		got := Parse(tc.in).Terms
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
	if !Parse("***").Empty() {
		t.Error("star-only query should be empty")
	}
}
