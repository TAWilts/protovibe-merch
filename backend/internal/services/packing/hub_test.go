package packing

import (
	"testing"
	"time"
)

func TestHubKeepsBandEventsIsolated(t *testing.T) {
	hub := NewHub()
	first, unsubscribeFirst := hub.Subscribe(11)
	defer unsubscribeFirst()
	second, unsubscribeSecond := hub.Subscribe(22)
	defer unsubscribeSecond()

	hub.Publish(11, 7)
	select {
	case revision := <-first:
		if revision != 7 {
			t.Fatalf("unexpected revision %d", revision)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subscribed band did not receive its revision")
	}
	select {
	case revision := <-second:
		t.Fatalf("other band received foreign revision %d", revision)
	default:
	}
}
