package remote

import (
	"context"
	"reflect"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

// TestListChannelsCapabilities: strings pass through unfiltered, and a
// response without the array (a server speaking an earlier draft) maps to a
// non-nil empty slice without an error.
func TestListChannelsCapabilities(t *testing.T) {
	b := newBus(t, &canned{status: 200, ctype: "application/json",
		body: `[{"name":"a","kind":"ordinary","capabilities":["bridged","future"]},{"name":"b","kind":"ordinary"}]`})
	cs, err := b.ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("got %d channels", len(cs))
	}
	if want := []ab2bus.ChannelCapability{"bridged", "future"}; !reflect.DeepEqual(cs[0].Capabilities, want) {
		t.Errorf("a capabilities = %#v, want %#v", cs[0].Capabilities, want)
	}
	if !cs[0].Has(ab2bus.ChannelBridged) {
		t.Error("a: Has(bridged) = false")
	}
	if cs[1].Capabilities == nil || len(cs[1].Capabilities) != 0 || cs[1].Has(ab2bus.ChannelBridged) {
		t.Errorf("b capabilities = %#v, want empty non-nil", cs[1].Capabilities)
	}
}
