// Code generated by github.com/Hartimer/loadingcache/cmd/typedcache, DO NOT EDIT.
package example

import (
	"fmt"
	"time"

	"github.com/Hartimer/loadingcache"
	"github.com/benbjohnson/clock"
)

type CoolCache interface {
	Get(key Name) (int64, error)
	Put(key Name, value int64)
	Invalidate(key Name, keys ...Name)
	InvalidateAll()
}

type CoolCacheOption func(CoolCache)

type LoadFunc func(Name) (int64, error)

type RemovalNotification struct {
	Key    Name
	Value  int64
	Reason loadingcache.RemovalReason
}

type RemovalListenerFunc func(RemovalNotification)

type internalImplementation struct {
	genericCache loadingcache.Cache
	cacheOptions []loadingcache.CacheOption
}

func Clock(clk clock.Clock) CoolCacheOption {
	return func(cache CoolCache) {
		if g, ok := cache.(*internalImplementation); ok {
			g.cacheOptions = append(g.cacheOptions, loadingcache.Clock(clk))
		}
	}
}

func ExpireAfterWrite(duration time.Duration) CoolCacheOption {
	return func(cache CoolCache) {
		if g, ok := cache.(*internalImplementation); ok {
			g.cacheOptions = append(g.cacheOptions, loadingcache.ExpireAfterWrite(duration))
		}
	}
}

func ExpireAfterRead(duration time.Duration) CoolCacheOption {
	return func(cache CoolCache) {
		if g, ok := cache.(*internalImplementation); ok {
			g.cacheOptions = append(g.cacheOptions, loadingcache.ExpireAfterRead(duration))
		}
	}
}

func Load(f LoadFunc) CoolCacheOption {
	return func(cache CoolCache) {
		if g, ok := cache.(*internalImplementation); ok {
			g.cacheOptions = append(g.cacheOptions, loadingcache.Load(func(key interface{}) (interface{}, error) {
				typedKey, ok := key.(Name)
				if !ok {
					return 0, fmt.Errorf("Key expeceted to be a Name but got %T", key)
				}
				return f(typedKey)
			}))
		}
	}
}

func MaxSize(maxSize int32) CoolCacheOption {
	return func(cache CoolCache) {
		if g, ok := cache.(*internalImplementation); ok {
			g.cacheOptions = append(g.cacheOptions, loadingcache.MaxSize(maxSize))
		}
	}
}

func RemovalListener(listener RemovalListenerFunc) CoolCacheOption {
	return func(cache CoolCache) {
		if g, ok := cache.(*internalImplementation); ok {
			g.cacheOptions = append(g.cacheOptions, loadingcache.RemovalListener(func(notification loadingcache.RemovalNotification) {
				typedNofication := RemovalNotification{Reason: notification.Reason}
				var ok bool
				typedNofication.Key, ok = notification.Key.(Name)
				if !ok {
					panic(fmt.Sprintf("Somehow the key is a %T instead of a Name", notification.Key))
				}
				typedNofication.Value, ok = notification.Value.(int64)
				if !ok {
					panic(fmt.Sprintf("Somehow the value is a %T instead of an int64", notification.Value))
				}
				listener(typedNofication)
			}))
		}
	}
}

func NewCache(options ...CoolCacheOption) CoolCache {
	internal := &internalImplementation{}
	for _, option := range options {
		option(internal)
	}

	internal.genericCache = loadingcache.NewGenericCache(internal.cacheOptions...)
	return internal
}

func (i *internalImplementation) Get(key Name) (int64, error) {
	val, err := i.genericCache.Get(key)
	if err != nil {
		return 0, err
	}
	typedVal, ok := val.(int64)
	if !ok {
		// TODO type mismatch error
	}
	return typedVal, nil
}

func (i *internalImplementation) Put(key Name, value int64) {
	i.genericCache.Put(key, value)
}

func (i *internalImplementation) Invalidate(key Name, keys ...Name) {
	genericKeys := make([]interface{}, len(keys))
	for i, k := range keys {
		genericKeys[i] = k
	}
	i.genericCache.Invalidate(key, genericKeys...)
}

func (i *internalImplementation) InvalidateAll() {
	i.genericCache.InvalidateAll()
}
