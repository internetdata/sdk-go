package internetdata

import "sync"

// flights is the board of addresses with a request in flight, so concurrent
// misses share one. A flight is keyed as the cache is, on the address and the
// user agent sent with it, so only misses for the same pair share a request. A cache that checks, misses and then fetches let every
// caller missing in the same moment fetch too: a middleware answering several
// requests from one visitor at once paid for the same lookup several times.
//
// It is ours rather than golang.org/x/sync/singleflight because a batch has to
// know which addresses it leads before it builds its chunks, and singleflight
// decides that inside DoChan and runs the function on a goroutine of its own.
type flights struct {
	mu sync.Mutex
	m  map[answerKey]*flight
}

// flight is one address's request. Every caller awaiting it reads the answer
// once done is closed.
type flight struct {
	done   chan struct{}
	result *Result
	err    error
}

// board joins the flight for each address that has one and starts one for each
// that has none, which the caller then sends and lands.
func (f *flights) board(keys []answerKey) (led, joined map[answerKey]*flight) {
	led = make(map[answerKey]*flight, len(keys))
	joined = make(map[answerKey]*flight)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = make(map[answerKey]*flight)
	}
	for _, key := range keys {
		if fl, ok := f.m[key]; ok {
			joined[key] = fl
			continue
		}
		fl := &flight{done: make(chan struct{})}
		f.m[key] = fl
		led[key] = fl
	}
	return led, joined
}

// land hands every waiter the answer and takes the address off the board. Cache
// a served answer FIRST: a caller that missed the cache just before it landed
// finds no flight after this, and reads the cache again before it sends.
func (f *flights) land(key answerKey, fl *flight, result *Result, err error) {
	f.mu.Lock()
	if f.m[key] == fl {
		delete(f.m, key)
	}
	f.mu.Unlock()
	fl.result, fl.err = result, err
	close(fl.done)
}

// answer is the landed outcome as one caller's own, a copy of the result as a
// cache hit is.
func (fl *flight) answer() (*Result, error) {
	if fl.err != nil {
		return nil, fl.err
	}
	result := *fl.result
	return &result, nil
}
