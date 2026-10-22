package cli

import (
	"errors"
	"os/user"
	"testing"
)

func TestOSUserName(t *testing.T) {
	tests := []struct {
		name string
		u    *user.User
		err  error
		want string
	}{
		{"plain", &user.User{Username: "efitz"}, nil, "efitz"},
		{"windows domain", &user.User{Username: `CORP\eric`}, nil, "eric"},
		{"slash", &user.User{Username: "a/b"}, nil, "a-b"},
		{"error", nil, errors.New("no passwd entry"), ""},
		{"empty", &user.User{}, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := osUserNameFrom(func() (*user.User, error) { return tc.u, tc.err })
			if got != tc.want {
				t.Fatalf("osUserNameFrom = %q, want %q", got, tc.want)
			}
		})
	}
}
