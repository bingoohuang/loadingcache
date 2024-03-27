// Package loadingcache provides a way for clients to create a cache capable
// of loading values on demand, should they get cache misses.
//
// You can configure the cache to expire entries after a certain amount elapses
// since the last write and/or read.
//
// This project is heavily inspired by Guava Cache (https://github.com/google/guava/wiki/CachesExplained).
//
// All errors are wrapped by github.com/pkg/errors.Wrap. If you which to check
// the type of it, please use github.com/pkg/errors.Is.
package loadingcache

import (
	"fmt"
	"sync"
	"time"

	"github.com/Hartimer/loadingcache/internal/stats"
	"github.com/benbjohnson/clock"
	"github.com/pkg/errors"
)

// ErrKeyNotFound represents an error indicating that the key was not found
var ErrKeyNotFound error = errors.New("Key not found")

// RemovalReason is an enum describing the causes for an entry to
// be removed from the cache.
type RemovalReason string

const (
	// RemovalReasonExplicit means the entry was explicitly invalidated
	RemovalReasonExplicit RemovalReason = "EXPLICIT"

	// RemovalReasonReplaced means the entry was replaced by a new one
	RemovalReasonReplaced RemovalReason = "REPLACED"

	// RemovalReasonExpired means the entry expired, e.g. too much time
	// since last read/write.
	RemovalReasonExpired RemovalReason = "EXPIRED"

	// RemovalReasonSize means the entry was removed due to the cache size.
	RemovalReasonSize RemovalReason = "SIZE"
)

// RemovalNotification is passed to listeners everytime an entry is removed
type RemovalNotification struct {
	Key    interface{}
	Value  interface{}
	Reason RemovalReason
}

// RemovalListener represents a removal listener
type RemovalListener func(RemovalNotification)

// Cache describe the base interface to interact with a generic cache.
//
// This interface reduces all keys and values to a generic interface{}.
type Cache interface {

	// Get returns the value associated with a given key. If no entry exists for
	// the provided key, loadingcache.ErrKeyNotFound is returned.
	Get(key interface{}) (interface{}, error)

	// Put adds a value to the cache identified by a key.
	// If a value already exists associated with that key, it
	// is replaced.
	Put(key interface{}, value interface{})

	// Invalidate removes keys from the cache. If a key does not exists it is a noop.
	Invalidate(key interface{}, keys ...interface{})

	// InvalidateAll invalidates all keys
	InvalidateAll()

	// Close cleans up any resources used by the cache
	Close()

	// Stats returns the curret stats
	Stats() Stats
}

// CacheOptions available options to initialize the cache
type CacheOptions struct {
	// Clock allows passing a custom clock to be used with the cache.
	//
	// This is useful for testing, where controlling time is important.
	Clock clock.Clock

	// ExpireAfterWrite configures the cache to expire entries after
	// a given duration after writing.
	ExpireAfterWrite time.Duration

	// ExpireAfterRead configures the cache to expire entries after
	// a given duration after reading.
	ExpireAfterRead time.Duration

	// Load configures a loading function
	Load LoadFunc

	// MaxSize limits the number of entries allowed in the cache.
	// If the limit is achieved, an eviction process will take place,
	// this means that eviction policies will be executed such as write
	// time, read time or random entry if no evection policy frees up
	// space.
	//
	// If the cache is sharded, MaxSize is applied to each shard,
	// meaning that the overall capacity will be MaxSize * ShardCount.
	MaxSize int32

	// RemovalListeners configures a removal listeners
	RemovalListeners []RemovalListener

	// ShardCount indicates how many shards will be used by the cache.
	// This allows some degree of parallelism in read and writing to the cache.
	//
	// If the shard count is greater than 1, then HashCodeFunc must be provided
	// otherwise the constructor will panic.
	ShardCount int

	// HashCodeFunc is a function that produces a hashcode of the key.
	//
	// See https://docs.oracle.com/en/java/javase/15/docs/api/java.base/java/lang/Object.html#hashCode()
	// for best practices surrounding hash code functions.
	HashCodeFunc func(key interface{}) int

	// BackgroundEvictFrequency controls if a background go routine should be created
	// which automatically evicts entries that have expired. If not speficied
	// no background goroutine will be created.
	//
	// The background go routine runs with the provided frequency.
	// To avoid go routine leaks, use the close function when you're done with the cache.
	BackgroundEvictFrequency time.Duration
}

func (c CacheOptions) expiresAfterRead() bool {
	return c.ExpireAfterRead > 0
}

func (c CacheOptions) expiresAfterWrite() bool {
	return c.ExpireAfterWrite > 0
}

// CacheOption describes an option that can configure the cache
type CacheOption func(Cache)

// LoadFunc represents a function that given a key, it returns a value or an error.
type LoadFunc func(interface{}) (interface{}, error)

type cacheEntry struct {
	key       interface{}
	value     interface{}
	lastRead  time.Time
	lastWrite time.Time
}

// New instantiates a new cache
func New(options CacheOptions) Cache {
	if options.Clock == nil {
		options.Clock = clock.New()
	}

	if options.ShardCount < 0 {
		panic("shard count must be non-negative")
	}

	switch options.ShardCount {
	case 0, 1:
		c := &genericCache{
			CacheOptions: options,
			data:         map[interface{}]*cacheEntry{},
			done:         make(chan struct{}),
			stats:        &stats.InternalStats{},
		}
		if options.BackgroundEvictFrequency > 0 {
			c.backgroundWg.Add(1)
			go c.runBackgroundEvict()
		}
		return c
	default:
		if options.HashCodeFunc == nil {
			panic("cannot have a sharded cache without a hashcode function")
		}
		singleShardOptions := options
		singleShardOptions.ShardCount = 1
		s := &shardedCache{
			CacheOptions: options,
			shards:       make([]Cache, options.ShardCount),
		}
		for i := 0; i < options.ShardCount; i++ {
			s.shards[i] = New(singleShardOptions)
		}
		return s
	}
}

type shardedCache struct {
	CacheOptions
	shards []Cache
}

func (s *shardedCache) Get(key interface{}) (interface{}, error) {
	val, err := s.shards[s.HashCodeFunc(key)%len(s.shards)].Get(key)
	return val, errors.Wrap(err, "")
}

func (s *shardedCache) Put(key interface{}, value interface{}) {
	s.shards[s.HashCodeFunc(key)%len(s.shards)].Put(key, value)
}

func (s *shardedCache) Invalidate(key interface{}, keys ...interface{}) {
	s.shards[s.HashCodeFunc(key)%len(s.shards)].Invalidate(key)
	for _, k := range keys {
		s.shards[s.HashCodeFunc(k)%len(s.shards)].Invalidate(k)
	}
}

func (s *shardedCache) InvalidateAll() {
	for _, shard := range s.shards {
		shard.InvalidateAll()
	}
}

func (s *shardedCache) Close() {
	for _, shard := range s.shards {
		shard.Close()
	}
}

func (s *shardedCache) Stats() Stats {
	statsSum := &stats.InternalStats{}
	for _, shard := range s.shards {
		switch typedCache := shard.(type) {
		case *genericCache:
			statsSum = statsSum.Add(typedCache.stats)
		default:
			panic(fmt.Sprintf("unsupported cache type %T", shard))
		}
	}
	return statsSum
}

// genericCache is an implementation of a cache where keys and values are
// of type interface{}
type genericCache struct {
	CacheOptions

	data     map[interface{}]*cacheEntry
	dataLock sync.RWMutex

	done         chan struct{}
	backgroundWg sync.WaitGroup

	stats *stats.InternalStats
}

func (g *genericCache) isExpired(entry *cacheEntry) bool {
	if g.expiresAfterRead() && entry.lastRead.Add(g.ExpireAfterRead).Before(g.Clock.Now()) {
		return true
	}
	if g.expiresAfterWrite() && entry.lastWrite.Add(g.ExpireAfterWrite).Before(g.Clock.Now()) {
		return true
	}
	return false
}

func (g *genericCache) Get(key interface{}) (interface{}, error) {
	g.dataLock.RLock()
	entry, exists := g.data[key]
	if !exists {
		g.dataLock.RUnlock()
		val, err := g.load(key)
		return val, errors.Wrap(err, "")
	}
	// Create a copy of the value to return to avoid concurrent updates
	toReturn := entry.value
	g.dataLock.RUnlock()

	if g.isExpired(entry) {
		g.concurrentEvict(key, RemovalReasonExpired)
		val, err := g.load(key)
		return val, errors.Wrap(err, "")
	}
	// It is possible that this will race. It will only be a problem
	// if the expiry thresholds have to be respected with a high
	// degree of precision (which is subjective).
	entry.lastRead = g.Clock.Now()
	g.stats.Hit()
	return toReturn, nil
}

func (g *genericCache) load(key interface{}) (interface{}, error) {
	g.dataLock.Lock()
	defer g.dataLock.Unlock()

	// It is possible that another call loaded the value for this key.
	// Let's do a double check if that was the case, since we have
	// the lock.
	if entry, exists := g.data[key]; exists {
		g.stats.Hit()
		return entry.value, nil
	} else if g.Load == nil {
		g.stats.Miss()
		return nil, errors.Wrap(ErrKeyNotFound, "")
	}

	loadStartTime := g.Clock.Now()
	val, err := g.Load(key)
	if err != nil {
		g.stats.LoadError()
		return nil, errors.Wrapf(err, "failed to load key %v", key)
	}
	g.stats.LoadTime(g.Clock.Now().Sub(loadStartTime))
	g.stats.LoadSuccess()
	g.internalPut(key, val)
	return val, nil
}

func (g *genericCache) concurrentEvict(key interface{}, reason RemovalReason) {
	g.dataLock.Lock()
	defer g.dataLock.Unlock()
	g.evict(key, reason)
}

func (g *genericCache) runBackgroundEvict() {
	ticker := g.Clock.Ticker(g.BackgroundEvictFrequency)
	defer ticker.Stop()
	defer g.backgroundWg.Done()
	for {
		select {
		case <-g.done:
			return
		case <-ticker.C:
			g.backgroundEvict()
		}
	}
}

// backgroundEvict performs a scan of the cache in search for expired entries
// and evicts them
func (g *genericCache) backgroundEvict() {
	g.dataLock.Lock()
	defer g.dataLock.Unlock()
	for key := range g.data {
		entry := g.data[key]
		if g.isExpired(entry) {
			// TODO: There's a possibility that we want to evict
			// in a go routine so we can get through
			// all expired entries as fast as possible without
			// having to sequentially wait for removal listeners.
			g.evict(entry.key, RemovalReasonExpired)
		}
	}
}

func (g *genericCache) evict(key interface{}, reason RemovalReason) {
	val, exists := g.data[key]
	if !exists {
		return
	}
	g.stats.Eviction()
	delete(g.data, key)

	if len(g.RemovalListeners) == 0 {
		return
	}
	notification := RemovalNotification{
		Key:    key,
		Value:  val.value,
		Reason: reason,
	}
	// Each removal listener is called on its own goroutine
	// so a slow one does not affect the others.
	// This could potentially be early optimization, but seems
	// simple enough.
	var listenerWg sync.WaitGroup
	listenerWg.Add(len(g.RemovalListeners))
	for i := range g.RemovalListeners {
		listener := g.RemovalListeners[i]
		go func() {
			defer listenerWg.Done()
			listener(notification)
		}()
	}
	listenerWg.Wait()
}

// internalPut actually saves the values into the internal structures.
// It does not handle any synchronization, leaving that to the caller.
func (g *genericCache) internalPut(key interface{}, value interface{}) {
	if g.MaxSize > 0 && int32(len(g.data)) >= g.MaxSize {
		// If eviction is needed it currently removes a random entry,
		// since maps do not have a deterministic order.
		// TODO: Apply smarter eviction policies if available
		for toEvict := range g.data {
			g.evict(toEvict, RemovalReasonSize)
			break
		}
	}
	g.data[key] = &cacheEntry{
		key:       key,
		value:     value,
		lastRead:  g.Clock.Now(),
		lastWrite: g.Clock.Now(),
	}
}

// preWriteCleanup does a pass through all entries to assess if any are expired
// and should be removed.
//
// If background cleanup os enabled, this becomes a noop.
func (g *genericCache) preWriteCleanup() {
	if g.BackgroundEvictFrequency > 0 {
		return
	}
	for key := range g.data {
		if g.isExpired(g.data[key]) {
			g.evict(key, RemovalReasonExpired)
		}
	}
}

func (g *genericCache) Put(key interface{}, value interface{}) {
	g.dataLock.Lock()
	defer g.dataLock.Unlock()
	g.preWriteCleanup()
	if _, exists := g.data[key]; exists {
		g.evict(key, RemovalReasonReplaced)
	}
	g.internalPut(key, value)
}

func (g *genericCache) Invalidate(key interface{}, keys ...interface{}) {
	g.dataLock.Lock()
	defer g.dataLock.Unlock()
	delete(g.data, key)
	for _, k := range keys {
		delete(g.data, k)
	}
}

func (g *genericCache) InvalidateAll() {
	g.dataLock.Lock()
	defer g.dataLock.Unlock()
	for key := range g.data {
		delete(g.data, key)
	}
}

func (g *genericCache) Close() {
	close(g.done)
	// Ensure that we wait for all background tasks to complete.
	g.backgroundWg.Wait()
}

func (g *genericCache) Stats() Stats {
	return g.stats
}
