package slackbridge

import (
	"fmt"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

func TestUserCacheTTL(t *testing.T) {
	var c userCache
	t0 := time.Unix(1000, 0)
	c.put("U1", slackapi.User{ID: "U1"}, t0)
	if _, ok := c.get("U1", t0.Add(userCacheTTL-time.Second)); !ok {
		t.Error("fresh entry missed")
	}
	if _, ok := c.get("U1", t0.Add(userCacheTTL)); ok {
		t.Error("expired entry served")
	}
}

func TestUserCacheBounded(t *testing.T) {
	var c userCache
	t0 := time.Unix(1000, 0)
	for i := range userCacheMax + 50 {
		c.put(fmt.Sprintf("U%d", i), slackapi.User{}, t0.Add(time.Duration(i)*time.Millisecond))
	}
	if n := c.len(); n > userCacheMax {
		t.Fatalf("cache holds %d entries, cap %d", n, userCacheMax)
	}
	// The newest entries survive, the oldest go.
	if _, ok := c.get(fmt.Sprintf("U%d", userCacheMax+49), t0.Add(time.Hour*0+time.Second)); !ok {
		t.Error("newest entry evicted")
	}
	if _, ok := c.get("U0", t0.Add(time.Second)); ok {
		t.Error("oldest entry kept past the cap")
	}
}

func TestUserCacheDropsExpiredOnPut(t *testing.T) {
	var c userCache
	t0 := time.Unix(1000, 0)
	for i := range 10 {
		c.put(fmt.Sprintf("U%d", i), slackapi.User{}, t0)
	}
	c.put("new", slackapi.User{}, t0.Add(userCacheTTL+time.Second))
	if n := c.len(); n != 1 {
		t.Errorf("expired entries kept: %d entries", n)
	}
}
