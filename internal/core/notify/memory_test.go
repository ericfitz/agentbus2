package notify

import (
	"testing"
	"time"
)

func TestMemoryFanOutAndCoalesce(t *testing.T) {
	m := NewMemory()
	c1, cancel1 := m.Subscribe()
	c2, cancel2 := m.Subscribe()
	defer cancel2()

	m.Publish(Hint{ChannelID: 1, Seq: 1})
	m.Publish(Hint{ChannelID: 1, Seq: 2})
	m.Publish(Hint{ChannelID: 1, Seq: 3})

	for i, c := range []<-chan Hint{c1, c2} {
		select {
		case <-c:
		default:
			t.Fatalf("subscriber %d got no hint", i)
		}
		select {
		case h := <-c:
			t.Fatalf("subscriber %d got a second hint %+v: hints must coalesce", i, h)
		default:
		}
	}

	cancel1()
	m.Publish(Hint{ChannelID: 2, Seq: 4})
	select {
	case h, ok := <-c1:
		if ok {
			t.Fatalf("cancelled subscriber received %+v", h)
		}
	default:
	}
	select {
	case <-c2:
	default:
		t.Fatal("the remaining subscriber missed a hint")
	}
	cancel1() // idempotent
}

func TestMemoryPublishNeverBlocks(t *testing.T) {
	m := NewMemory()
	_, cancel := m.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			m.Publish(Hint{Seq: int64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
}

func TestMemoryNoSubscribers(t *testing.T) {
	NewMemory().Publish(Hint{Seq: 1}) // must not panic
}
