package rules

// MaxSubjectRunes bounds a message subject (v1 ADR 0010).
const MaxSubjectRunes = MaxLineRunes

// NormalizeSubject trims s; an empty result means no subject. A line break or
// more than MaxSubjectRunes characters is a validation error naming the field.
func NormalizeSubject(s string) (string, error) {
	return NormalizeLine("subject", s)
}
