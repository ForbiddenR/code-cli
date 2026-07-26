package webfetch

import (
	"container/list"
	"sync"
	"time"
)

type FetchedContent struct {
	Content       string
	Bytes         int
	Code          int
	CodeText      string
	ContentType   string
	PersistedPath string
	PersistedSize int
	Redirect      bool
}

type contentCacheEntry struct {
	key       string
	value     FetchedContent
	expiresAt time.Time
	weight    int
}

type ContentCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	maxWeight int
	weight    int
	now       func() time.Time
	items     map[string]*list.Element
	order     *list.List
}

func NewContentCache(ttl time.Duration, maxWeight int, now func() time.Time) *ContentCache {
	return &ContentCache{ttl: ttl, maxWeight: maxWeight, now: now, items: make(map[string]*list.Element), order: list.New()}
}

func (c *ContentCache) Get(key string) (FetchedContent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		return FetchedContent{}, false
	}
	entry := element.Value.(*contentCacheEntry)
	if !c.now().Before(entry.expiresAt) {
		c.remove(element)
		return FetchedContent{}, false
	}
	c.order.MoveToFront(element)
	return entry.value, true
}

func (c *ContentCache) Set(key string, value FetchedContent) {
	weight := value.Bytes
	if value.ContentType != "" && containsHTML(value.ContentType) {
		weight = len([]byte(value.Content))
	}
	if weight < 1 {
		weight = 1
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.remove(element)
	}
	if weight > c.maxWeight {
		return
	}
	entry := &contentCacheEntry{key: key, value: value, expiresAt: c.now().Add(c.ttl), weight: weight}
	element := c.order.PushFront(entry)
	c.items[key] = element
	c.weight += weight
	for c.weight > c.maxWeight {
		c.remove(c.order.Back())
	}
}

func (c *ContentCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.order.Init()
	c.weight = 0
}

func (c *ContentCache) remove(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*contentCacheEntry)
	delete(c.items, entry.key)
	c.weight -= entry.weight
	c.order.Remove(element)
}

type allowedDomainEntry struct {
	domain    string
	expiresAt time.Time
}

type AllowedDomainCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	items      map[string]*list.Element
	order      *list.List
}

func NewAllowedDomainCache(ttl time.Duration, maxEntries int, now func() time.Time) *AllowedDomainCache {
	return &AllowedDomainCache{ttl: ttl, maxEntries: maxEntries, now: now, items: make(map[string]*list.Element), order: list.New()}
}

func (c *AllowedDomainCache) Get(domain string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[domain]
	if !ok {
		return false
	}
	entry := element.Value.(*allowedDomainEntry)
	if !c.now().Before(entry.expiresAt) {
		delete(c.items, domain)
		c.order.Remove(element)
		return false
	}
	c.order.MoveToFront(element)
	return true
}

func (c *AllowedDomainCache) Set(domain string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[domain]; ok {
		c.order.Remove(element)
		delete(c.items, domain)
	}
	entry := &allowedDomainEntry{domain: domain, expiresAt: c.now().Add(c.ttl)}
	c.items[domain] = c.order.PushFront(entry)
	for len(c.items) > c.maxEntries {
		oldest := c.order.Back()
		entry := oldest.Value.(*allowedDomainEntry)
		delete(c.items, entry.domain)
		c.order.Remove(oldest)
	}
}

func (c *AllowedDomainCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.order.Init()
}
