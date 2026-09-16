// Local validation on the database methods, as distinct from the shared
// conformance corpus in conformance_test.go.

package internetdata

import (
	"errors"
	"strings"
	"testing"
)

// Format is a defined string type, so an invalid one is constructible and the
// generated client would happily send it. Every method that takes a format must
// refuse before the network sees it, and the transport asserts that.
func TestAnUnpublishedFormatIsRejectedWithoutTouchingTheNetwork(t *testing.T) {
	stub := newStub(nil)
	client := newTestClient(t, stub)

	for _, bad := range []Format{"zip", "", "MMDB"} {
		if _, err := client.Database.Checksums(t.Context(), "bogon_ip_v1", bad); !isBadFormat(t, err) {
			t.Errorf("Checksums(%q): %v", bad, err)
		}
		if _, err := client.Database.DownloadURL(t.Context(), "bogon_ip_v1", bad); !isBadFormat(t, err) {
			t.Errorf("DownloadURL(%q): %v", bad, err)
		}
	}
	if n := stub.count(); n != 0 {
		t.Errorf("an invalid format must cost no request, got %d", n)
	}
}

func TestValidReportsThePublishedFormats(t *testing.T) {
	for _, ok := range []Format{FormatCSVGZ, FormatMMDB} {
		if !ok.Valid() {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []Format{"zip", "", "CSVGZ"} {
		if bad.Valid() {
			t.Errorf("%q must not be valid", bad)
		}
	}
}

func isBadFormat(t *testing.T, err error) bool {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindBadRequest {
		return false
	}
	// The message has to name what IS allowed, or the caller learns only that
	// they were wrong.
	return strings.Contains(e.Message, "csvgz") && strings.Contains(e.Message, "mmdb")
}
