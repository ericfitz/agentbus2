package search

import (
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestPrepareValidation(t *testing.T) {
	cases := []struct {
		name      string
		in        bus.SearchInput
		embedder  bool
		wantCode  bus.Code
		wantMode  string
		wantCount int
		wantOff   int
	}{
		{"blank query", bus.SearchInput{Query: "   "}, false, bus.CodeValidation, "", 0, 0},
		{"stars only", bus.SearchInput{Query: "* **"}, false, bus.CodeValidation, "", 0, 0},
		{"unknown mode", bus.SearchInput{Query: "a", Mode: "fuzzy"}, true, bus.CodeValidation, "", 0, 0},
		{"negative cursor", bus.SearchInput{Query: "a", Cursor: "-1"}, false, bus.CodeValidation, "", 0, 0},
		{"junk cursor", bus.SearchInput{Query: "a", Cursor: "x"}, false, bus.CodeValidation, "", 0, 0},
		{"bad tag pattern", bus.SearchInput{Query: "a", Tags: []string{"Bad Tag"}}, false, bus.CodeValidation, "", 0, 0},
		{"default mode no embedder", bus.SearchInput{Query: "a"}, false, "", "text", 100, 0},
		{"default mode embedder", bus.SearchInput{Query: "a"}, true, "", "both", 100, 0},
		{"count capped", bus.SearchInput{Query: "a", Count: 5000, Cursor: "7"}, false, "", "text", 1000, 7},
		{"explicit semantic", bus.SearchInput{Query: "a", Mode: "semantic", Count: 3}, false, "", "semantic", 3, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := prepare(c.in, c.embedder)
			if c.wantCode != "" {
				if bus.CodeOf(err) != c.wantCode {
					t.Fatalf("err = %v, want code %q", err, c.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.mode != c.wantMode || p.count != c.wantCount || p.offset != c.wantOff {
				t.Fatalf("got mode=%q count=%d offset=%d", p.mode, p.count, p.offset)
			}
		})
	}
}

func TestSemanticQueryDropsStars(t *testing.T) {
	p, err := prepare(bus.SearchInput{Query: "alpha* beta"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.semanticText(); got != "alpha beta" {
		t.Fatalf("semanticText = %q", got)
	}
}
