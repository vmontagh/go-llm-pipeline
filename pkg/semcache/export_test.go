package semcache

// This file exposes internal cache state to the black-box _test package so
// tests can assert bounded memory and force the stale-vector skip path without
// widening the public API.

// PartitionCount reports how many semantic partition stores currently exist. It
// lets tests assert that a bounded cache does not accumulate partition stores.
func (c *Cache) PartitionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.stores)
}

// DropEntryKeepVector removes key's entry from the key map and ordering while
// leaving its indexed vector in place, simulating a stale vector left behind by
// a failed eviction delete so tests can exercise the skip path in getSemantic.
func (c *Cache) DropEntryKeepVector(key Key) {
	hash, err := hashKey(key)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[hash]
	if !ok {
		return
	}
	c.order.Remove(el)
	delete(c.byKey, hash)
}
