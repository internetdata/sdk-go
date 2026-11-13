package internetdata

import "sync"

// flights is the board of addresses with a request in flight, so concurrent
// misses share one. A cache that checks, misses and then fetches let every
// caller missing in the same moment fetch too: a middleware answering several
// requests from one visitor at once paid for the same lookup several times.
//
// It is ours rather than golang.org/x/sync/singleflight because a batch has to
// know which addresses it leads before it builds its chunks, and singleflight
// decides that inside DoChan and runs the function on a goroutine of its own.
type flights struct {
	mu sync.Mutex
	m  map[string]*flight
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
func (f *flights) board(ips []string) (led, joined map[string]*flight) {
	led = make(map[string]*flight, len(ips))
	joined = make(map[string]*flight)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = make(map[string]*flight)
	}
	for _, ip := range ips {
		if fl, ok := f.m[ip]; ok {
			joined[ip] = fl
			continue
		}
		fl := &flight{done: make(chan struct{})}
		f.m[ip] = fl
		led[ip] = fl
	}
	return led, joined
}

// land hands every waiter the answer and takes the address off the board. Cache
// a served answer FIRST: a caller that missed the cache just before it landed
// finds no flight after this, and reads the cache again before it sends.
func (f *flights) land(ip string, fl *flight, result *Result, err error) {
	f.mu.Lock()
	if f.m[ip] == fl {
		delete(f.m, ip)
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
