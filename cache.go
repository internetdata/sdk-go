package internetdata

import (
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// reducedTTL is the longest a spent trial's or a reached cap's answer is cached.
const reducedTTL = time.Minute

// answerCache holds each address's answer for the client's TTL. A reduced answer,
// a spent trial's or a reached cap's, ends with a new UTC month or a raised cap, so
// it stays a minute at most. It is still cached, or a busy process would send
// every repeat of an address to the API.
type answerCache struct {
	lru *expirable.LRU[string, cachedAnswer]
	ttl time.Duration
}

type cachedAnswer struct {
	result  Result
	expires time.Time
}

func newAnswerCache(size int, ttl time.Duration) *answerCache {
	return &answerCache{lru: expirable.NewLRU[string, cachedAnswer](size, nil, ttl), ttl: ttl}
}

// Get answers the fresh answer held for ip.
func (c *answerCache) Get(ip string) (Result, bool) {
	held, ok := c.lru.Get(ip)
	if !ok {
		return Result{}, false
	}
	if !time.Now().Before(held.expires) {
		c.lru.Remove(ip)
		return Result{}, false
	}
	return held.result, true
}

// Add holds result for ip as long as its flags allow.
func (c *answerCache) Add(ip string, result Result) {
	c.lru.Add(ip, cachedAnswer{result: result, expires: time.Now().Add(c.lifeOf(result))})
}

// expiry is when the answer held for ip goes stale.
func (c *answerCache) expiry(ip string) (time.Time, bool) {
	held, ok := c.lru.Peek(ip)
	return held.expires, ok
}

func (c *answerCache) lifeOf(result Result) time.Duration {
	reduced := (result.TrialExpired != nil && *result.TrialExpired) ||
		(result.CapReached != nil && *result.CapReached)
	if reduced {
		return min(c.ttl, reducedTTL)
	}
	return c.ttl
}
