package rules

import (
	"math"
	"slices"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	v := []float32{0, 1, -1.5, 3.25, math.MaxFloat32}
	b := EncodeVec(v)
	if len(b) != 4*len(v) {
		t.Fatalf("encoded length %d", len(b))
	}
	if got := DecodeVec(b); !slices.Equal(got, v) {
		t.Fatalf("round trip: %v", got)
	}
	if got := DecodeVec(nil); len(got) != 0 {
		t.Fatalf("nil: %v", got)
	}
}

func TestDotOfNormalized(t *testing.T) {
	a := []float32{3, 4}
	Normalize(a)
	if d := Dot(a, a); math.Abs(d-1) > 1e-6 {
		t.Fatalf("equal unit vectors: %v", d)
	}
	x, y := []float32{1, 0}, []float32{0, 1}
	if d := Dot(x, y); d != 0 {
		t.Fatalf("orthogonal: %v", d)
	}
	// A shorter second vector contributes only its length.
	if d := Dot([]float32{1, 1, 1}, []float32{2}); d != 2 {
		t.Fatalf("length mismatch: %v", d)
	}
	zero := []float32{0, 0}
	Normalize(zero)
	if zero[0] != 0 || zero[1] != 0 {
		t.Fatalf("zero vector must stay zero: %v", zero)
	}
}

func TestEmbedText(t *testing.T) {
	if got := EmbedText("", "body"); got != "body" {
		t.Fatalf("no subject: %q", got)
	}
	if got := EmbedText("title", "body"); got != "title\n\nbody" {
		t.Fatalf("subject: %q", got)
	}
}

type hit struct {
	seq   int64
	score float64
}

func TestRRF(t *testing.T) {
	seqOf := func(h hit) int64 { return h.seq }
	set := func(h *hit, s float64) { h.score = s }
	text := []hit{{seq: 1}, {seq: 2}, {seq: 3}}
	sem := []hit{{seq: 3}, {seq: 2}, {seq: 4}}
	got := RRF(text, sem, seqOf, set)
	order := make([]int64, len(got))
	for i, h := range got {
		order[i] = h.seq
	}
	// 3: 1/63+1/61 beats 2: 1/62+1/62; then 1: 1/61, 4: 1/63.
	if !slices.Equal(order, []int64{3, 2, 1, 4}) {
		t.Fatalf("order %v", order)
	}
	if want := 2.0 / 62; math.Abs(got[1].score-want) > 1e-12 {
		t.Fatalf("score %v want %v", got[1].score, want)
	}
	// Equal scores order by seq ascending.
	tie := RRF([]hit{{seq: 9}}, []hit{{seq: 5}}, seqOf, set)
	if tie[0].seq != 5 || tie[1].seq != 9 {
		t.Fatalf("tie: %v", tie)
	}
	if got := RRF(nil, nil, seqOf, set); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}

func TestSearchPageBounds(t *testing.T) {
	if SearchPageDefault != 100 || SearchPageMax != 1000 {
		t.Fatalf("%d %d", SearchPageDefault, SearchPageMax)
	}
}
