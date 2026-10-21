package sessions

import "testing"

func TestDisplayName(t *testing.T) {
	cases := []struct {
		name  string
		hint  string
		taken []string
		want  string
	}{
		{"none taken", "", nil, "pat"},
		{"base taken", "", []string{"pat"}, "pat-2"},
		{"base and second taken", "", []string{"pat", "pat-2"}, "pat-3"},
		{"hint, none taken", "impl", nil, "pat/impl"},
		{"hint taken", "impl", []string{"pat/impl"}, "pat/impl-2"},
		{"only the second is taken", "", []string{"pat-2"}, "pat"},
		{"hint is independent of the base", "impl", []string{"pat", "pat-2"}, "pat/impl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DisplayName("pat", c.hint, c.taken); got != c.want {
				t.Fatalf("DisplayName(pat, %q, %v) = %q, want %q", c.hint, c.taken, got, c.want)
			}
		})
	}
}
