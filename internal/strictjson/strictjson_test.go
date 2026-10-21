package strictjson

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inner struct {
	A string `json:"a"`
	B int    `json:"b,omitempty"`
}

type Embedded struct {
	E string `json:"e"`
}

type outer struct {
	Embedded
	Name    string            `json:"name"`
	Skip    string            `json:"-"`
	Plain   string            // json name "Plain"
	hidden  string            //nolint:unused // unexported, never a member
	In      inner             `json:"in"`
	Ptr     *inner            `json:"ptr"`
	List    []inner           `json:"list"`
	Arr     [2]inner          `json:"arr"`
	Map     map[string]inner  `json:"map"`
	Strs    map[string]string `json:"strs"`
	Any     any               `json:"any"`
	Raw     json.RawMessage   `json:"raw"`
	When    time.Time         `json:"when"`
	Numbers []float64         `json:"numbers"`
}

func TestCheck(t *testing.T) {
	ty := reflect.TypeFor[outer]()
	ok := []string{
		`{}`,
		`{"e":"x","name":"n","Plain":"p","in":{"a":"x","b":1}}`,
		`{"ptr":{"a":"x"}}`,
		`{"list":[{"a":"x"},{"a":"y","b":2}],"arr":[{"a":"x"},{}]}`,
		`{"map":{"k":{"a":"x"},"l":{"a":"y"}},"strs":{"a":"1","A":"2"}}`,
		// opaque values are not walked
		`{"any":{"a":1,"a":2,"X":[{"q":1,"q":2}]},"raw":{"z":1,"z":2},"when":"2026-10-21T00:00:00Z"}`,
		// a value of the wrong shape is the decoder's business
		`{"in":[1,{"a":1,"a":2}],"list":{"x":1},"map":[1],"numbers":[1,2.5e3,-0]}`,
		`{"name":null,"in":null,"list":null}`,
	}
	for _, b := range ok {
		if err := Check([]byte(b), ty); err != nil {
			t.Errorf("Check(%s) = %v, want nil", b, err)
		}
	}
	bad := []struct{ body, want string }{
		{`{"Name":"n"}`, `unknown member "Name"`},
		{`{"name":"n","name":"m"}`, `member "name" repeated`},
		{`{"plain":"p"}`, `unknown member "plain"`},
		{`{"Skip":"s"}`, `unknown member "Skip"`},
		{`{"hidden":"h"}`, `unknown member "hidden"`},
		{`{"Embedded":{"e":"x"}}`, `unknown member "Embedded"`},
		{`{"E":"x"}`, `unknown member "E"`},
		{`{"e":"x","e":"y"}`, `member "e" repeated`},
		{`{"in":{"A":"x"}}`, `in: unknown member "A"`},
		{`{"in":{"a":"x","a":"y"}}`, `in: member "a" repeated`},
		{`{"ptr":{"B":1}}`, `ptr: unknown member "B"`},
		{`{"list":[{"a":"x"},{"a":"y","a":"z"}]}`, `list[1]: member "a" repeated`},
		{`{"arr":[{},{"A":1}]}`, `arr[1]: unknown member "A"`},
		{`{"map":{"k":{"a":"x"},"k":{"a":"y"}}}`, `map: key "k" repeated`},
		{`{"map":{"k":{"A":"x"}}}`, `map["k"]: unknown member "A"`},
		{`{"strs":{"a":"1","a":"2"}}`, `strs: key "a" repeated`},
		{`[]`, "not a JSON object"},
		{`null`, "not a JSON object"},
		{`"x"`, "not a JSON object"},
		{``, "not a JSON object"},
		{`{"name":`, "EOF"},
	}
	for _, tc := range bad {
		err := Check([]byte(tc.body), ty)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Check(%s) = %v, want error containing %q", tc.body, err, tc.want)
		}
	}
}

func TestCheckNonStructRoots(t *testing.T) {
	list := reflect.TypeFor[[]inner]()
	if err := Check([]byte(`[{"a":"x"},{"b":1}]`), list); err != nil {
		t.Errorf("slice root: %v", err)
	}
	if err := Check([]byte(`[{"a":"x"},{"a":"x","a":"y"}]`), list); err == nil || !strings.Contains(err.Error(), `[1]: member "a" repeated`) {
		t.Errorf("slice root repeated member: %v", err)
	}
	if err := Check([]byte(`null`), list); err != nil {
		t.Errorf("null slice is the decoder's business: %v", err)
	}
	m := reflect.TypeFor[map[string]int]()
	if err := Check([]byte(`{"a":1,"a":2}`), m); err == nil || !strings.Contains(err.Error(), `key "a" repeated`) {
		t.Errorf("map root repeated key: %v", err)
	}
	p := reflect.TypeFor[*inner]()
	if err := Check([]byte(`{"A":1}`), p); err == nil || !strings.Contains(err.Error(), `unknown member "A"`) {
		t.Errorf("pointer root: %v", err)
	}
}
