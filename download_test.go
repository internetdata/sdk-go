// The download path, exercised against a real HTTP origin rather than the stub
// transport: the whole point of these methods is what the client does with a
// 302 and with a body too large to hold, and a stub answers neither.

package internetdata

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var small = []byte("start_ip,end_ip\n10.0.0.0,10.255.255.255\n")

func TestDownloadFileFollowsTheRedirectAndWritesTheFile(t *testing.T) {
	origin := newOrigin(t, originConfig{})
	path := filepath.Join(t.TempDir(), "bogon_ip_v1.csv.gz")

	written, err := origin.client.DownloadFile(t.Context(), "bogon_ip_v1", FormatCSVGZ, path)
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}

	if written != int64(len(small)) {
		t.Errorf("wrote %d byte(s), want %d", written, len(small))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the download back: %v", err)
	}
	if !bytes.Equal(got, small) {
		t.Errorf("the file holds %q, want %q", got, small)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("the .part file outlived a successful transfer")
	}
	origin.assertPaths(t, pathDownload, "/blob")
}

func TestDownloadStreamsIntoAWriter(t *testing.T) {
	origin := newOrigin(t, originConfig{})
	var sink bytes.Buffer

	written, err := origin.client.Download(t.Context(), "bogon_ip_v1", FormatCSVGZ, &sink)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if written != int64(len(small)) {
		t.Errorf("wrote %d byte(s), want %d", written, len(small))
	}
	if !bytes.Equal(sink.Bytes(), small) {
		t.Errorf("the writer holds %q, want %q", sink.Bytes(), small)
	}
}

func TestDownloadBytesAgreesWithTheStreamedCopy(t *testing.T) {
	origin := newOrigin(t, originConfig{})
	var streamed bytes.Buffer
	if _, err := origin.client.Download(
		t.Context(), "bogon_ip_v1", FormatCSVGZ, &streamed); err != nil {
		t.Fatalf("Download: %v", err)
	}

	got, err := origin.client.DownloadBytes(t.Context(), "bogon_ip_v1", FormatCSVGZ)
	if err != nil {
		t.Fatalf("DownloadBytes: %v", err)
	}

	if !bytes.Equal(got, streamed.Bytes()) {
		t.Errorf("DownloadBytes = %q, and the streamed copy = %q", got, streamed.Bytes())
	}
	if !bytes.Equal(got, small) {
		t.Errorf("DownloadBytes = %q, want %q", got, small)
	}
}

// A body shorter than the Content-Length it declared is a transfer that was cut
// off, and handing back what did arrive would look like a smaller database.
func TestDownloadBytesRefusesATruncatedBody(t *testing.T) {
	origin := newOrigin(t, originConfig{blobBytes: 4 << 20, dieAfterBytes: 1 << 20})

	got, err := origin.client.DownloadBytes(t.Context(), "bogon_ip_v1", FormatCSVGZ)

	if err == nil {
		t.Fatalf("a truncated transfer returned %d byte(s) and no error", len(got))
	}
	if got != nil {
		t.Errorf("a failed DownloadBytes handed back %d byte(s)", len(got))
	}
}

// The key authorizes the API call that mints the link. The link is presigned and
// authorizes itself, so forwarding the key on would hand a credential to a host
// that has no business seeing it.
func TestTheAPIKeyReachesTheAPIAndNeverObjectStorage(t *testing.T) {
	origin := newOrigin(t, originConfig{})

	path := filepath.Join(t.TempDir(), "keys.csv.gz")
	if _, err := origin.client.DownloadFile(
		t.Context(), "bogon_ip_v1", FormatCSVGZ, path); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}

	api := origin.request(t, pathDownload)
	if !strings.Contains(api.Header.Get("Authorization"), testKey) {
		t.Error("the API request carried no key, so this proves nothing about the second one")
	}
	storage := origin.request(t, "/blob")
	for name, values := range storage.Header {
		for _, value := range values {
			if strings.Contains(value, testKey) {
				t.Errorf("the key leaked to object storage in %s: %s", name, value)
			}
		}
	}
	if strings.Contains(storage.URL.RawQuery, testKey) {
		t.Errorf("the key leaked into the storage query string: %s", storage.URL.RawQuery)
	}
}

// The assertion that matters most: a body far larger than any sane buffer has to
// move through the process without ever being resident. The threshold is an
// eighth of the payload, so a buffering implementation cannot slip under it.
func TestALargeBodyIsStreamedNotBuffered(t *testing.T) {
	const size = 2 << 30
	const ceiling = size / 8
	origin := newOrigin(t, originConfig{blobBytes: size})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	written, err := origin.client.Download(t.Context(), "bogon_ip_v1", FormatCSVGZ, io.Discard)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if written != size {
		t.Errorf("transferred %d byte(s) of a %d byte body", written, size)
	}
	// Memory taken from the OS, not the live heap: a buffering implementation
	// frees its buffer at the end of the call but cannot hide having held it.
	grew := int64(after.HeapSys) - int64(before.HeapSys)
	if grew > ceiling {
		t.Errorf("the heap grew %d MiB for a %d MiB body, so the body was held",
			grew>>20, size>>20)
	}
}

func TestObjectStorageRefusingTheLinkIsNotReportedAsACatalogFailure(t *testing.T) {
	origin := newOrigin(t, originConfig{storageStatus: http.StatusForbidden})

	_, err := origin.client.DownloadBytes(t.Context(), "bogon_ip_v1", FormatCSVGZ)

	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("DownloadBytes error is %T (%v), want *Error", err, err)
	}
	if apiErr.Kind != KindForbidden {
		t.Errorf("Kind = %q, want %q", apiErr.Kind, KindForbidden)
	}
	if apiErr.Retryable() {
		t.Error("a refused link is not worth retrying")
	}
	if !strings.Contains(apiErr.Message, "object storage") {
		t.Errorf("Message = %q, and does not say which host refused", apiErr.Message)
	}
}

// A truncated file that looks complete is worse than no file: the next run reads
// it as a whole database. The bytes land beside the destination and the name
// only appears on success.
func TestATransferThatDiesPartWayLeavesNothingAtTheDestination(t *testing.T) {
	origin := newOrigin(t, originConfig{blobBytes: 4 << 20, dieAfterBytes: 1 << 20})
	path := filepath.Join(t.TempDir(), "half-a-database.csv.gz")

	_, err := origin.client.DownloadFile(t.Context(), "bogon_ip_v1", FormatCSVGZ, path)

	if err == nil {
		t.Fatal("a transfer that lost its connection reported success")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a truncated file is sitting at the destination")
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("the .part file was left behind")
	}
}

// The half of the .part guard a cleanup step cannot fake: a destination opened
// directly is truncated before the first byte arrives, so yesterday's good copy
// is gone whether or not the refresh then succeeds.
func TestAFailedRefreshLeavesThePreviousCopyIntact(t *testing.T) {
	origin := newOrigin(t, originConfig{blobBytes: 4 << 20, dieAfterBytes: 1 << 20})
	path := filepath.Join(t.TempDir(), "bogon_ip_v1.csv.gz")
	if err := os.WriteFile(path, small, 0o644); err != nil {
		t.Fatalf("seeding the previous copy: %v", err)
	}

	if _, err := origin.client.DownloadFile(
		t.Context(), "bogon_ip_v1", FormatCSVGZ, path); err == nil {
		t.Fatal("a transfer that lost its connection reported success")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the previous copy is gone: %v", err)
	}
	if !bytes.Equal(got, small) {
		t.Errorf("the previous copy now holds %d byte(s), want the %d it had", len(got), len(small))
	}
}

const testKey = "secret-key"

type originConfig struct {
	blobBytes     int
	storageStatus int
	dieAfterBytes int
}

// Serves the API's 302 and the object storage it points at, on one origin, and
// records every request so a test can assert what did NOT happen.
type testOrigin struct {
	client *Client

	mu   sync.Mutex
	seen []*http.Request
}

func newOrigin(t *testing.T, cfg originConfig) *testOrigin {
	t.Helper()
	origin := &testOrigin{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin.record(r)
		// Absolute, and carrying a signature, as the real 302 to object storage
		// is: a presigned URL is on another host entirely, so nothing here may
		// lean on a relative one.
		origin.serve(w, r, server.URL+"/blob?X-Amz-Signature=abc", cfg)
	}))
	t.Cleanup(server.Close)

	client, err := New(testKey, WithBaseURL(server.URL), WithRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	origin.client = client
	return origin
}

func (o *testOrigin) serve(w http.ResponseWriter, r *http.Request, blobURL string, cfg originConfig) {
	if r.URL.Path == pathDownload {
		http.Redirect(w, r, blobURL, http.StatusFound)
		return
	}
	if r.URL.Path != "/blob" {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"rc":"UNKNOWN_DATASET"}`)
		return
	}
	if cfg.storageStatus != 0 && cfg.storageStatus != http.StatusOK {
		w.WriteHeader(cfg.storageStatus)
		fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
		return
	}
	if cfg.blobBytes == 0 {
		w.Header().Set("Content-Length", fmt.Sprint(len(small)))
		w.Write(small)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(cfg.blobBytes))
	if cfg.dieAfterBytes > 0 {
		// Promises blobBytes and delivers fewer, then drops the connection: the
		// transfer fails with the destination already part written.
		io.CopyN(w, filler{}, int64(cfg.dieAfterBytes))
		panic(http.ErrAbortHandler)
	}
	io.CopyN(w, filler{}, int64(cfg.blobBytes))
}

func (o *testOrigin) record(r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, r.Clone(r.Context()))
}

func (o *testOrigin) paths() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pathsLocked()
}

func (o *testOrigin) pathsLocked() []string {
	paths := make([]string, len(o.seen))
	for i, r := range o.seen {
		paths[i] = r.URL.Path
	}
	return paths
}

func (o *testOrigin) assertPaths(t *testing.T, want ...string) {
	t.Helper()
	got := o.paths()
	if len(got) != len(want) {
		t.Fatalf("the origin saw %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d went to %q, want %q", i, got[i], want[i])
		}
	}
}

func (o *testOrigin) request(t *testing.T, path string) *http.Request {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, r := range o.seen {
		if r.URL.Path == path {
			return r
		}
	}
	t.Fatalf("the origin was never asked for %q, it saw %v", path, o.pathsLocked())
	return nil
}

// A reader with no end, so a multi-gigabyte body costs the server nothing to
// serve and the test measures the client rather than the fixture.
type filler struct{}

func (filler) Read(p []byte) (int, error) {
	return len(p), nil
}
