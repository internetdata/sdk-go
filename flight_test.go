package internetdata

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

// Long enough that a call started a quarter of it later is still concurrent.
const stagger = 400 * time.Millisecond

func (s *stubTransport) sent() ([]string, [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	urls := make([]string, len(s.calls))
	for i, call := range s.calls {
		urls[i] = call.url
	}
	return urls, slices.Clone(s.batched)
}

// A middleware looks the visitor up on every request a page fires, all at once,
// so concurrent misses for one address must share one request.
func TestConcurrentMissesForOneAddressShareOneRequest(t *testing.T) {
	stub := newStub(okRoutes("45.83.91.1"))
	stub.delay = 200 * time.Millisecond
	client := newTestClient(t, stub)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			result, err := client.Lookup(t.Context(), "45.83.91.1")
			if err == nil && result.IP != "45.83.91.1" {
				err = errors.New("answered " + result.IP)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("a caller: %v", err)
		}
	}
	if n := stub.count(); n != 1 {
		t.Errorf("20 concurrent callers sent %d requests, want 1", n)
	}
}

// A failure is handed to every caller waiting on it and cached for none, so the
// next call asks again.
func TestASharedFailureReachesEveryWaiterAndIsNotCached(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		"45.83.91.1": {status: http.StatusForbidden, body: map[string]string{"error": "forbidden"}},
	})
	stub.delay = 200 * time.Millisecond
	client := newTestClient(t, stub, WithRetries(0))

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Go(func() {
			_, err := client.Lookup(t.Context(), "45.83.91.1")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Kind != KindForbidden {
			t.Errorf("a waiter got %v, want the one forbidden answer", err)
		}
	}
	if n := stub.count(); n != 1 {
		t.Fatalf("sent %d requests, want 1", n)
	}

	stub.mu.Lock()
	stub.routes["45.83.91.1"] = stubRoute{body: answerAbout("45.83.91.1", map[string]any{"is_vpn": true})}
	stub.mu.Unlock()
	result, err := client.Lookup(t.Context(), "45.83.91.1")
	if err != nil || !BoolValue(result.IsVpn) {
		t.Fatalf("asked again: %v %v", result, err)
	}
	if n := stub.count(); n != 2 {
		t.Errorf("sent %d requests, want 2: the failure was cached", n)
	}
}

// A batch awaits the lookup already in flight for one of its addresses rather
// than sending that address again.
func TestABatchAwaitsALookupInFlight(t *testing.T) {
	stub := newStub(okRoutes("45.83.91.1", "45.83.91.2"))
	stub.delay = stagger
	client := newTestClient(t, stub)

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := client.Lookup(t.Context(), "45.83.91.1"); err != nil {
			t.Errorf("Lookup: %v", err)
		}
	})
	time.Sleep(stagger / 4)
	got, err := client.LookupBatch(t.Context(), []string{"45.83.91.1", "45.83.91.2"})
	wg.Wait()

	if err != nil || got["45.83.91.1"].Err != nil || got["45.83.91.2"].Err != nil {
		t.Fatalf("LookupBatch: %v %+v", err, got)
	}
	calls, batched := stub.sent()
	if len(calls) != 2 || len(batched) != 1 || !slices.Equal(batched[0], []string{"45.83.91.2"}) {
		t.Errorf("sent %v with bodies %v, want the lookup and a batch of 45.83.91.2 alone", calls, batched)
	}
}

// A lookup awaits the batch in flight that carries its address.
func TestALookupAwaitsABatchInFlight(t *testing.T) {
	stub := newStub(okRoutes("45.83.91.1", "45.83.91.2"))
	stub.delay = stagger
	client := newTestClient(t, stub)

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := client.LookupBatch(t.Context(), []string{"45.83.91.1", "45.83.91.2"}); err != nil {
			t.Errorf("LookupBatch: %v", err)
		}
	})
	time.Sleep(stagger / 4)
	result, err := client.Lookup(t.Context(), "45.83.91.2")
	wg.Wait()

	if err != nil || result.IP != "45.83.91.2" {
		t.Fatalf("Lookup: %v %v", result, err)
	}
	if n := stub.count(); n != 1 {
		t.Errorf("sent %d requests, want the batch alone", n)
	}
}

// A lookup that joined a batch takes the batch's answer for its address, a
// failed one included, which the cache never holds for it to fall back on.
func TestALookupTakesTheFailureOfTheBatchItJoined(t *testing.T) {
	routes := okRoutes("45.83.91.1")
	routes["45.83.91.2"] = stubRoute{status: http.StatusForbidden, body: map[string]string{"error": "forbidden"}}
	stub := newStub(routes)
	stub.delay = stagger
	client := newTestClient(t, stub)

	var wg sync.WaitGroup
	wg.Go(func() { _, _ = client.LookupBatch(t.Context(), []string{"45.83.91.1", "45.83.91.2"}) })
	time.Sleep(stagger / 4)
	_, err := client.Lookup(t.Context(), "45.83.91.2")
	wg.Wait()

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != KindForbidden {
		t.Errorf("Lookup: %v, want the batch's forbidden answer", err)
	}
	if n := stub.count(); n != 1 {
		t.Errorf("sent %d requests, want the batch alone", n)
	}
}

// The request runs detached from the caller that led it, so that caller giving
// up returns at once and fails nobody waiting on it.
func TestACallerGivingUpFailsNobodyElse(t *testing.T) {
	stub := newStub(okRoutes("45.83.91.1", "45.83.91.2"))
	stub.delay = stagger
	client := newTestClient(t, stub)

	short, cancel := context.WithTimeout(t.Context(), stagger/2)
	defer cancel()
	var wg sync.WaitGroup
	var result *Result
	var err error
	wg.Go(func() {
		time.Sleep(stagger / 4)
		result, err = client.Lookup(t.Context(), "45.83.91.1")
	})
	start := time.Now()
	_, gaveUp := client.Lookup(short, "45.83.91.1")
	took := time.Since(start)
	wg.Wait()

	if !errors.Is(gaveUp, context.DeadlineExceeded) || took > stagger*3/4 {
		t.Errorf("the leader returned %v after %s, want its own deadline at once", gaveUp, took)
	}
	if err != nil || result.IP != "45.83.91.1" {
		t.Errorf("the waiter got %v %v, want the answer", result, err)
	}

	// A batch given up part way fails no lookup that joined it either.
	short, cancel = context.WithTimeout(t.Context(), stagger/2)
	defer cancel()
	wg.Go(func() {
		time.Sleep(stagger / 4)
		result, err = client.Lookup(t.Context(), "45.83.91.2")
	})
	got, batchErr := client.LookupBatch(short, []string{"45.83.91.2"})
	wg.Wait()
	if !errors.Is(batchErr, context.DeadlineExceeded) || !errors.Is(got["45.83.91.2"].Err, context.DeadlineExceeded) {
		t.Errorf("the batch returned %v %+v, want its own deadline", batchErr, got)
	}
	if err != nil || result.IP != "45.83.91.2" {
		t.Errorf("the lookup that joined the batch got %v %v, want the answer", result, err)
	}
	if n := stub.count(); n != 2 {
		t.Errorf("sent %d requests, want one per address", n)
	}
}

// Without a cache every lookup is served, as WithoutCache promises.
func TestAClientWithoutACacheSharesNothing(t *testing.T) {
	stub := newStub(okRoutes("45.83.91.1"))
	stub.delay = 100 * time.Millisecond
	client := newTestClient(t, stub, WithoutCache())

	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() { _, _ = client.Lookup(t.Context(), "45.83.91.1") })
	}
	for range 2 {
		wg.Go(func() { _, _ = client.LookupBatch(t.Context(), []string{"45.83.91.1"}) })
	}
	wg.Wait()

	if n := stub.count(); n != 5 {
		t.Errorf("sent %d requests, want 5", n)
	}
}
