package rules

import (
	"encoding/binary"
	"math"
	"sort"
)

// Normalize scales v to unit length in place; a zero vector is left as is.
func Normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
}

// Dot is the dot product of a and b over the shorter length; for normalized
// vectors it is the cosine score (Decision 51).
func Dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		if i < len(b) {
			s += float64(a[i]) * float64(b[i])
		}
	}
	return s
}

// EncodeVec serializes v as little-endian float32.
func EncodeVec(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(x))
	}
	return buf
}

// DecodeVec is the inverse of EncodeVec; trailing bytes short of a float are ignored.
func DecodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// EmbedText is what a memory revision embeds: subject and content together
// when it has a subject, else the content alone (v1 ADR 0010).
func EmbedText(subject, content string) string {
	if subject == "" {
		return content
	}
	return subject + "\n\n" + content
}

// SearchPageDefault and SearchPageMax bound a search page.
const (
	SearchPageDefault = 100
	SearchPageMax     = 1000
)

// RRF merges two rankings by reciprocal rank fusion (k = 60). Items are
// identified by seqOf; the first occurrence supplies the item, setScore
// stores the fused score, and ties order by seq ascending.
func RRF[T any](text, sem []T, seqOf func(T) int64, setScore func(*T, float64)) []T {
	score := map[int64]float64{}
	bySeq := map[int64]T{}
	for _, list := range [][]T{text, sem} {
		for rank, h := range list {
			s := seqOf(h)
			score[s] += 1 / float64(60+rank+1)
			if _, ok := bySeq[s]; !ok {
				bySeq[s] = h
			}
		}
	}
	out := make([]T, 0, len(bySeq))
	for s, h := range bySeq {
		setScore(&h, score[s])
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := seqOf(out[i]), seqOf(out[j])
		if score[si] == score[sj] {
			return si < sj
		}
		return score[si] > score[sj]
	})
	return out
}
