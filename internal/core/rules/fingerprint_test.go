package rules

import "testing"

func TestFingerprintKeyOrderIndependent(t *testing.T) {
	a, err := Fingerprint(map[string]string{"x": "1", "y": "2"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fingerprint(map[string]string{"y": "2", "x": "1"})
	if err != nil || a != b {
		t.Fatalf("%q vs %q (%v)", a, b, err)
	}
	if len(a) != 64 {
		t.Fatalf("sha256 hex length %d", len(a))
	}
	c, _ := Fingerprint(map[string]string{"x": "1", "y": "3"})
	if c == a {
		t.Fatal("different payloads share a fingerprint")
	}
}

func TestFingerprintUnmarshalable(t *testing.T) {
	if _, err := Fingerprint(make(chan int)); err == nil {
		t.Fatal("a channel must not fingerprint")
	}
}
