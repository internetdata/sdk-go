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
//
// An answer is held under the address and the user agent sent with it: one
// with an agent carries that agent's verdict, so it is kept apart from the
// address's plain answer and from another agent's.
type answerCache struct {
	lru *expirable.LRU[answerKey, cachedAnswer]
	ttl time.Duration
}

// answerKey is what an answer was asked for: an address as it is sent, and
// the user agent sent with it, empty for none.
type answerKey struct {
	ip    string
	agent string
}

type cachedAnswer struct {
	result  Result
	expires time.Time
}

func newAnswerCache(size int, ttl time.Duration) *answerCache {
	return &answerCache{lru: expirable.NewLRU[answerKey, cachedAnswer](size, nil, ttl), ttl: ttl}
}

// Get answers the fresh answer held for key.
func (c *answerCache) Get(key answerKey) (Result, bool) {
	held, ok := c.lru.Get(key)
	if !ok {
		return Result{}, false
	}
	if !time.Now().Before(held.expires) {
		c.lru.Remove(key)
		return Result{}, false
	}
	return held.result, true
}

// Add holds result for key as long as its flags allow.
func (c *answerCache) Add(key answerKey, result Result) {
	c.lru.Add(key, cachedAnswer{result: result, expires: time.Now().Add(c.lifeOf(result))})
}

// expiry is when the answer held for key goes stale.
func (c *answerCache) expiry(key answerKey) (time.Time, bool) {
	held, ok := c.lru.Peek(key)
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
