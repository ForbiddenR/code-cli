package webfetch

import (
	"testing"
	"time"
)

func TestContentCacheExpiryAndLRUEviction(t *testing.T) {
	now := time.Unix(100, 0)
	cache := NewContentCache(time.Minute, 6, func() time.Time { return now })
	cache.Set("a", FetchedContent{Content: "aaa", Bytes: 3, ContentType: "text/plain"})
	cache.Set("b", FetchedContent{Content: "bb", Bytes: 2, ContentType: "text/plain"})
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("expected a cache hit")
	}
	cache.Set("c", FetchedContent{Content: "ccc", Bytes: 3, ContentType: "text/plain"})
	if _, ok := cache.Get("b"); ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("recently used entry was evicted")
	}
	now = now.Add(time.Minute)
	if _, ok := cache.Get("a"); ok {
		t.Fatal("expired entry remained cached")
	}
}

func TestContentCacheUsesConvertedHTMLWeight(t *testing.T) {
	cache := NewContentCache(time.Minute, 4, time.Now)
	cache.Set("html", FetchedContent{Content: "four", Bytes: 100, ContentType: "text/html"})
	if _, ok := cache.Get("html"); !ok {
		t.Fatal("converted HTML weight should fit")
	}
}

func TestAllowedDomainCacheExpiryAndBound(t *testing.T) {
	now := time.Unix(100, 0)
	cache := NewAllowedDomainCache(time.Minute, 2, func() time.Time { return now })
	cache.Set("a.example")
	cache.Set("b.example")
	if !cache.Get("a.example") {
		t.Fatal("expected domain cache hit")
	}
	cache.Set("c.example")
	if cache.Get("b.example") {
		t.Fatal("least recently used domain was not evicted")
	}
	now = now.Add(time.Minute)
	if cache.Get("a.example") {
		t.Fatal("expired domain remained cached")
	}
}
