// The whole surface, against a real deployment.
//
// The transfer is budgeted before it starts. Metadata publishes a size per
// format, and that size is checked against the ceiling below FIRST, so a
// mistaken database id can never quietly pull one of the multi-gigabyte
// databases through CI.

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	internetdata "github.com/internetdata/sdk-go"
)

const (
	// The two the SDK CI organization licenses, and the smallest published:
	// hundreds of bytes each, so a suite may fetch every artifact it is
	// entitled to and still cost nothing.
	databaseID = "bogon_ip_v1"
	otherID    = "bogon_asn_v1"
	format     = internetdata.FormatCSVGZ
	// 8 MiB against a database of a few hundred bytes. Four orders of magnitude
	// of headroom, so tripping it means the suite is pointed somewhere
	// unintended, which is exactly when a transfer must not go ahead. The
	// published catalog reaches 5.34 GiB, and this is the only thing standing
	// between a typo and pulling one of those.
	ceiling = 8 << 20
	// A real, public catalog id this organization holds no licence for. Repoint
	// it if that ever changes; the refusal is the assertion, not the id.
	unlicensedID = "hosting_ip_v1"
)

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestTheCatalogAnswersTheSchemaTheClientWasGeneratedFrom(t *testing.T) {
	client, rec := clientFor(t)

	databases, err := client.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(databases) == 0 {
		t.Fatal("the catalog is empty")
	}
	// Named first, and with what actually arrived, because every typed
	// assertion below reads as a zero value when the payload disagrees, and a
	// bare "want a string" costs a whole CI cycle to interpret.
	served := servedKeys(t, rec)
	for _, want := range []string{"base", "standing", "versions"} {
		if !slices.Contains(served, want) {
			t.Fatalf("the payload carries %s, and Database declares %s",
				strings.Join(served, ", "), want)
		}
	}
	if slices.Contains(served, "docsGroup") {
		t.Error("docsGroup is a docs-site slug and must not be published as API surface")
	}

	standings := []internetdata.Standing{
		internetdata.StandingLicensed,
		internetdata.StandingExpired,
		internetdata.StandingUnlicensed,
	}
	rights := []internetdata.Redistribution{
		internetdata.RedistributionEvaluation,
		internetdata.RedistributionInternal,
		internetdata.RedistributionRedistribute,
	}
	var licensed []string
	for _, d := range databases {
		if d.Base == "" || d.Name == "" {
			t.Errorf("a family carries no base or name: %+v", d)
		}
		if !slices.Contains(standings, d.Standing) {
			t.Errorf("%s carries an undocumented standing %q", d.Base, d.Standing)
		}
		// Null when there is no licence at all, which is most of the catalog
		// for this organization, so the pointer is read before the value.
		if d.Redistribution != nil && !slices.Contains(rights, *d.Redistribution) {
			t.Errorf("%s carries an undocumented right %q", d.Base, *d.Redistribution)
		}
		if d.Standing == internetdata.StandingUnlicensed && d.Redistribution != nil {
			t.Errorf("%s is unlicensed and still carries a right", d.Base)
		}
		// The point of the family shape: a licence covers the family, and these
		// are the ids the download, checksum and metadata calls take.
		if len(d.Versions) == 0 {
			t.Errorf("%s carries no versions", d.Base)
		}
		for _, v := range d.Versions {
			if v.ID == "" || v.Version == 0 {
				t.Errorf("%s has a version with no id or number: %+v", d.Base, v)
			}
			if len(v.Formats) == 0 {
				t.Errorf("%s carries no formats", v.ID)
			}
		}
		if d.Standing == internetdata.StandingLicensed {
			licensed = append(licensed, d.Base)
		}
	}
	t.Logf("licensed: %s (of %d published families)", strings.Join(licensed, ", "), len(databases))
	for _, want := range []string{"bogon_ip", "bogon_asn"} {
		if !slices.Contains(licensed, want) {
			t.Errorf("this organization should license %s, and the catalog says %v", want, licensed)
		}
	}
}

// A private family is one built for a single customer, and it is ABSENT from
// this organization's listing rather than present with standing `unlicensed`.
// This suite cannot name one without publishing the customer relationship, so
// what it asserts is the shape that makes the rule checkable at all: every
// standing served is one of the three, so an absent family is genuinely absent
// rather than hiding behind a fourth value nobody handles.
func TestTheListingIsWhatTheServerSaidAndNothingElse(t *testing.T) {
	client, rec := clientFor(t)

	databases, err := client.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var envelope struct {
		Databases []struct {
			Base string `json:"base"`
		} `json:"databases"`
	}
	if err := json.Unmarshal(rec.rawBody(t, "/api/v2/database/list"), &envelope); err != nil {
		t.Fatalf("parsing the listing: %v", err)
	}

	if len(databases) != len(envelope.Databases) {
		t.Fatalf("List returned %d families and the wire carried %d",
			len(databases), len(envelope.Databases))
	}
	for i, d := range databases {
		if d.Base != envelope.Databases[i].Base {
			t.Errorf("family %d is %q on the wire and %q from the client",
				i, envelope.Databases[i].Base, d.Base)
		}
	}
}

func TestADatabaseTheOrganizationDoesNotLicenseIsRefusedCleanly(t *testing.T) {
	client, rec := clientFor(t)

	_, err := client.DownloadURL(t.Context(), unlicensedID, format)

	var apiErr *internetdata.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("a refusal must arrive as the library error type, got %T (%v). If %s is now"+
			" licensed to this organization, point this at one that is not", err, err, unlicensedID)
	}
	if apiErr.Kind != internetdata.KindForbidden {
		t.Errorf("Kind = %q, want %q", apiErr.Kind, internetdata.KindForbidden)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want 403", apiErr.StatusCode)
	}
	if apiErr.Retryable() {
		t.Error("a licence refusal is not worth retrying")
	}
	// The API says WHICH refusal this is (`{"rc":"NOT_LICENSED"}`), and that is
	// the difference between "buy it" and "renew it". Falling back to the status
	// means the client never read the envelope.
	if apiErr.Message != "NOT_LICENSED" && apiErr.Message != "LICENSE_EXPIRED" {
		t.Errorf("Message = %q, want the API's own rc", apiErr.Message)
	}
	if issued := len(rec.seen()); issued != 1 {
		t.Errorf("issued %d request(s), and a 4xx must not be retried", issued)
	}
}

// The v2 answer to a download is a 302 to object storage, so the link can be
// handed to anything: it authorizes itself and carries no credential of ours.
func TestDownloadURLHandsBackACredentialFreeLink(t *testing.T) {
	client, rec := clientFor(t)

	link, err := client.DownloadURL(t.Context(), databaseID, format)
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}

	if strings.Contains(link, stagingKey()) {
		t.Fatal("the link carries the API key, so it is not safe to hand on")
	}
	if strings.HasPrefix(link, staging+"/api/") {
		t.Errorf("the link points back at the API rather than at object storage")
	}
	// One request: the redirect was read, not followed.
	if issued := len(rec.seen()); issued != 1 {
		t.Errorf("issued %d request(s), want 1", issued)
	}

	// A plain client with no credential at all: the link either works on its
	// own or it is not the credential-free thing this method promises.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, link, nil)
	if err != nil {
		t.Fatalf("building the storage request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetching the link: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("the link answered %d to an unauthenticated GET", res.StatusCode)
	}
}

func TestDownloadFileStreamsARealDatabaseToDiskIntact(t *testing.T) {
	dl := transferred(t)

	if dl.written <= 0 {
		t.Fatal("nothing was transferred")
	}
	info, err := os.Stat(dl.path)
	if err != nil {
		t.Fatalf("the download is not on disk: %v", err)
	}
	if info.Size() != dl.written {
		t.Errorf("the file is %d bytes and the method reported %d", info.Size(), dl.written)
	}
	if _, err := os.Stat(dl.path + ".part"); !os.IsNotExist(err) {
		t.Error("the .part file outlived a successful transfer")
	}
	body, err := os.ReadFile(dl.path)
	if err != nil {
		t.Fatalf("reading the download back: %v", err)
	}
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Error("the payload is not gzip")
	}

	if !hexDigest.MatchString(dl.checksums.SHA256) {
		t.Fatalf("sha256 = %q, so the checksums did not unwrap past the envelope",
			dl.checksums.SHA256)
	}
	if got := digest(body); got != dl.checksums.SHA256 {
		t.Errorf("the bytes hash to %s, and the API publishes %s", got, dl.checksums.SHA256)
	}

	// The presigned URL authorizes itself, so the request that follows the 302
	// must carry no credential.
	storage := 0
	for _, f := range dl.facts {
		if f.origin == staging {
			continue
		}
		storage++
		if f.carriedKey {
			t.Errorf("the API key was sent to object storage at %s", f.origin)
		}
	}
	if storage == 0 {
		t.Error("nothing was fetched from object storage, so no 302 was followed")
	}
}

func TestDownloadBytesAgreesWithTheStreamedCopy(t *testing.T) {
	dl := transferred(t)
	client, _ := clientFor(t)

	raw, err := client.DownloadBytes(t.Context(), databaseID, format)
	if err != nil {
		t.Fatalf("DownloadBytes: %v", err)
	}

	if int64(len(raw)) != dl.written {
		t.Errorf("the in-memory copy is %d bytes and the streamed one %d", len(raw), dl.written)
	}
	if got := digest(raw); got != dl.checksums.SHA256 {
		t.Errorf("the in-memory copy hashes to %s, and the API publishes %s", got, dl.checksums.SHA256)
	}
}

// The second licensed family, so the suite covers a database whose metadata
// declares only one format and proves the ceiling check is read per format
// rather than off a single hardcoded number.
func TestTheOtherLicensedDatabaseIsAlsoReachable(t *testing.T) {
	client, _ := clientFor(t)

	meta, err := client.Metadata(t.Context(), otherID)
	if err != nil {
		t.Fatalf("Metadata(%s): %v", otherID, err)
	}
	if meta.ID != otherID {
		t.Fatalf("Metadata answered about %q, want %q", meta.ID, otherID)
	}
	size := publishedSize(t, meta, otherID)

	raw, err := client.DownloadBytes(t.Context(), otherID, format)
	if err != nil {
		t.Fatalf("DownloadBytes(%s): %v", otherID, err)
	}
	if int64(len(raw)) != size {
		t.Errorf("%s is %d bytes and metadata publishes %d", otherID, len(raw), size)
	}
	t.Logf("%s.%s: %d bytes, %d entries, updated %s",
		otherID, format, len(raw), meta.Entries, meta.Updated.Format("2006-01-02"))
}

// Refusals are listed too, so the denial the licence test just earned is what
// answers "it stopped working". The write is fire and forget on the server, so
// what is asserted is the shape rather than the presence of one specific row.
func TestTheDownloadHistoryReadsBack(t *testing.T) {
	client, _ := clientFor(t)

	history, err := client.Downloads(t.Context(), 20)
	if err != nil {
		t.Fatalf("Downloads: %v", err)
	}

	outcomes := []internetdata.DownloadOutcome{
		internetdata.DownloadOutcomeOK,
		internetdata.DownloadOutcomeUnauthorized,
		internetdata.DownloadOutcomeDenied,
		internetdata.DownloadOutcomeExpired,
		internetdata.DownloadOutcomeUnknown,
		internetdata.DownloadOutcomeUnavailable,
	}
	for i, d := range history {
		if d.DatasetID == "" || d.Created.IsZero() {
			t.Errorf("attempt %d carries no id or timestamp: %+v", i, d)
		}
		if !slices.Contains(outcomes, d.Outcome) {
			t.Errorf("attempt %d carries an undocumented outcome %q", i, d.Outcome)
		}
		// Newest first, which is what makes an unlimited read of the top of the
		// list worth anything.
		if i > 0 && d.Created.After(history[i-1].Created) {
			t.Errorf("attempt %d is newer than the one before it", i)
		}
	}
	t.Logf("%d attempt(s) in the history", len(history))
}

type transfer struct {
	written   int64
	path      string
	checksums *internetdata.Checksums
	facts     []fact
}

// Memoized so the transfer tests share one download rather than pulling the
// database twice each. Held in a directory of the package's own, because a
// t.TempDir belongs to whichever test happened to ask first and would be gone
// before the other read it.
var shared *transfer

func transferred(t *testing.T) *transfer {
	t.Helper()
	if shared != nil {
		return shared
	}
	client, rec := clientFor(t)

	meta, err := client.Metadata(t.Context(), databaseID)
	if err != nil {
		t.Fatalf("Metadata(%s): %v", databaseID, err)
	}
	if meta.ID != databaseID {
		t.Fatalf("Metadata answered about %q, want %q", meta.ID, databaseID)
	}
	size := publishedSize(t, meta, databaseID)

	path := filepath.Join(tempDir(t), databaseID+".csv.gz")
	written, err := client.DownloadFile(t.Context(), databaseID, format, path)
	if err != nil {
		t.Fatalf("DownloadFile(%s): %v", databaseID, err)
	}
	// Read after the transfer, so a rebuild between the two calls shows up as a
	// digest mismatch rather than passing against a digest of nothing.
	checksums, err := client.Checksums(t.Context(), databaseID, format)
	if err != nil {
		t.Fatalf("Checksums(%s): %v", databaseID, err)
	}
	t.Logf("%s.%s: %d bytes, metadata says %d, %d entries, updated %s",
		databaseID, format, written, size, meta.Entries, meta.Updated.Format("2006-01-02"))

	shared = &transfer{written: written, path: path, checksums: checksums, facts: rec.seen()}
	return shared
}

// The budget, read from the API rather than assumed, and checked BEFORE any
// transfer starts.
func publishedSize(t *testing.T, meta *internetdata.DatabaseMetadata, id string) int64 {
	t.Helper()
	size, ok := meta.Size[string(format)]
	if !ok {
		t.Fatalf("%s publishes no %s size to check a transfer against, only %v",
			id, format, slices.Sorted(maps.Keys(meta.Size)))
	}
	if size <= 0 || size > ceiling {
		t.Fatalf("%s is %d bytes, past the %d ceiling, so it is not transferred",
			id, size, ceiling)
	}
	return size
}

// The keys the payload actually carried, which the typed decode cannot show: an
// undocumented field silently disappears into a struct that has no home for it.
func servedKeys(t *testing.T, rec *recorder) []string {
	t.Helper()
	var envelope struct {
		Databases []map[string]json.RawMessage `json:"databases"`
	}
	if err := json.Unmarshal(rec.rawBody(t, "/api/v2/database/list"), &envelope); err != nil {
		t.Fatalf("parsing the listing: %v", err)
	}
	keys := map[string]bool{}
	for _, database := range envelope.Databases {
		for key := range database {
			keys[key] = true
		}
	}
	return slices.Sorted(maps.Keys(keys))
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// A scratch directory for the package rather than for one test, and removed by
// TestMain once every test has had its look at what was transferred.
var tempRoot string

func tempDir(t *testing.T) string {
	t.Helper()
	if tempRoot != "" {
		return tempRoot
	}
	dir, err := os.MkdirTemp("", "internetdata-integration-")
	if err != nil {
		t.Fatalf("creating a scratch directory: %v", err)
	}
	tempRoot = dir
	return dir
}

func removeTempDir() {
	if tempRoot != "" {
		os.RemoveAll(tempRoot)
	}
}
