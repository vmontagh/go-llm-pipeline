package semcache

// This file exposes internal cache state to the black-box _test package so
// tests can assert bounded memory without widening the public API.

// PartitionCount reports how many semantic partition stores currently exist. It
// lets tests assert that a bounded cache does not accumulate partition stores.
func (c *Cache) PartitionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.stores)
}
