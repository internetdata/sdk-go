package internetdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/oapi-codegen/runtime/types"

	"github.com/internetdata/sdk-go/v2/internal/api"
)

// DatabaseAPI is the licensed database catalog and its downloads, reached
// through Client.Database.
//
// Spelled DatabaseAPI rather than Database because Database is already this
// API's own shape for one database family, and it is what the other
// InternetData SDKs call this type.
type DatabaseAPI struct {
	api      *api.ClientWithResponses
	transfer *http.Client
	retries  int
}

// List is the published catalog, with your organization's license beside each
// family. A license covers a family, while a download names one of its
// versions, so the ids the download and checksum calls take come from
// Database.Versions.
//
// This is the server's answer for this key, so a listing held from one key is
// not an answer for another.
func (d *DatabaseAPI) List(ctx context.Context) ([]Database, error) {
	return withRetry(ctx, d.retries, func() ([]Database, error) {
		answer, err := decodeAnswer[databaseList](d.api.ListDatabases(ctx))
		if err != nil {
			return nil, err
		}
		return answer.Databases, nil
	})
}

// Metadata is what is inside one database: its columns per format, sample rows,
// the row count and the byte size of each file. It carries Updated and Entries
// without downloading anything, so poll it to decide whether today's build is
// worth fetching, and read Size to budget a transfer before starting one.
//
// One document describes every format the database is built in, which is why
// there is no format argument.
func (d *DatabaseAPI) Metadata(ctx context.Context, id string) (*DatabaseMetadata, error) {
	return withRetry(ctx, d.retries, func() (*DatabaseMetadata, error) {
		return decodeAnswer[DatabaseMetadata](d.api.DatabaseMetadataV2(ctx, &api.DatabaseMetadataV2Params{ID: id}))
	})
}

// Checksums are the digests of one published file, for verifying a download.
// All four the exporter writes are returned, because which of them a caller
// wants is not this library's decision.
func (d *DatabaseAPI) Checksums(ctx context.Context, id string, format Format) (*Checksums, error) {
	if err := checkFormat(format); err != nil {
		return nil, err
	}
	return withRetry(ctx, d.retries, func() (*Checksums, error) {
		answer, err := decodeAnswer[checksumsAnswer](d.api.DatabaseChecksumV2(ctx, &api.DatabaseChecksumV2Params{
			ID:     id,
			Format: api.DatabaseFormat(format),
		}))
		if err != nil {
			return nil, err
		}
		// The digests hang under a `checksums` key rather than sitting at the
		// top level beside `id` and `format`.
		sums := answer.Checksums
		return &Checksums{
			MD5: sums.Md5, SHA1: sums.Sha1, SHA256: sums.Sha256, SHA512: sums.Sha512,
		}, nil
	})
}

// Downloads is your organization's recent download attempts, newest first. A
// limit of zero or less takes the API's own default of 50, and it is clamped to
// 200.
//
// Refusals are listed too: a denial is what answers "it stopped working", and
// its absence answers nothing.
func (d *DatabaseAPI) Downloads(ctx context.Context, limit int) ([]DownloadAttempt, error) {
	return withRetry(ctx, d.retries, func() ([]DownloadAttempt, error) {
		params := &api.ListDownloadsParams{}
		if limit > 0 {
			params.Limit = &limit
		}
		answer, err := decodeAnswer[downloadList](d.api.ListDownloads(ctx, params))
		if err != nil {
			return nil, err
		}
		return answer.Downloads, nil
	})
}

// The envelopes three answers arrive in, as the generated client declares them.
type (
	databaseList struct {
		Databases []Database `json:"databases"`
	}
	downloadList struct {
		Downloads []DownloadAttempt `json:"downloads"`
	}
	checksumsAnswer struct {
		Checksums api.DBChecksums    `json:"checksums"`
		Format    api.DatabaseFormat `json:"format"`
		ID        string             `json:"id"`
	}
)

// Reads the JSON answer a database call returns. Anything but a 2xx is
// classified by its status, and a 2xx that is not the answer - a proxy's HTML
// page, a cut-off or empty body, or one without a member the answer requires -
// is a server error carrying that status, retried like a 5xx. Through v2.6.0 an
// HTML page was a bad_request sent once, a cut-off body a network error with
// no status, and `{}` an empty catalog handed back as the answer.
func decodeAnswer[T any](res *http.Response, err error) (*T, error) {
	if err != nil {
		return nil, errorFromTransport(err)
	}
	body, err := readBody(res)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, errorFromResponse(res.StatusCode, res.Header, body)
	}
	var answer T
	var raw any
	if json.Unmarshal(body, &answer) != nil || json.Unmarshal(body, &raw) != nil ||
		!complete(reflect.TypeFor[T](), raw) {
		return nil, unreadable(res.StatusCode, "the response was not the answer this call returns")
	}
	return &answer, nil
}

var unmarshaler = reflect.TypeFor[json.Unmarshaler]()

// Reports whether raw, a decoded JSON value, carries every member t requires,
// at every depth. The generated types spell a member the spec requires and does
// not allow to be null as neither a pointer nor omitempty, so that is the rule:
// present, and not null. json.Unmarshal checks neither, and reads a missing or
// null member as its zero value.
func complete(t reflect.Type, raw any) bool {
	if raw == nil {
		return t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface
	}
	if t.Kind() == reflect.Pointer {
		return complete(t.Elem(), raw)
	}
	if reflect.PointerTo(t).Implements(unmarshaler) {
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		for i := range t.NumField() {
			field := t.Field(i)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if !field.IsExported() || name == "" || name == "-" {
				continue
			}
			value, present := object[name]
			required := field.Type.Kind() != reflect.Pointer && !strings.Contains(options, "omitempty")
			if (required && !present) || (present && !complete(field.Type, value)) {
				return false
			}
		}
	case reflect.Slice:
		items, ok := raw.([]any)
		if !ok {
			return false
		}
		for _, item := range items {
			if !complete(t.Elem(), item) {
				return false
			}
		}
	case reflect.Map:
		entries, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		for _, entry := range entries {
			if !complete(t.Elem(), entry) {
				return false
			}
		}
	}
	return true
}

// Checksums are the digests of one published file.
//
// Spelled out rather than aliased to the generated struct, whose Md5 and Sha256
// are not the names a Go caller expects, and which every other InternetData and
// VPNDetection SDK writes the same way.
type Checksums struct {
	MD5    string `json:"md5"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
	SHA512 string `json:"sha512"`
}

// Format is a format a database is published in. Not every one is built in
// every format: the _provider catalogs are keyed by provider id rather than by
// IP range, so no MMDB exists for them, and asking for one is an error rather
// than an empty answer. DatabaseVersion.Formats says which exist.
type Format string

const (
	FormatCSVGZ Format = "csvgz"
	FormatMMDB  Format = "mmdb"
)

// Valid reports whether f is a format the API publishes.
//
// Format is a defined string type, so Format("zip") compiles: the constants
// above document the vocabulary without closing it. Callers taking a format
// from a flag, a config file or a model should check it here.
func (f Format) Valid() bool {
	return f == FormatCSVGZ || f == FormatMMDB
}

// Rejects a format the API does not publish before the network sees it.
//
// Without this the call costs a round trip and returns a 400 whose message
// names nothing the caller can act on. Ruby, PHP, Python, Java and Perl all
// reject locally; this is Go catching up.
func checkFormat(f Format) error {
	if f.Valid() {
		return nil
	}
	return &Error{
		Kind: KindBadRequest,
		Message: fmt.Sprintf("invalid format %q; must be one of %q, %q",
			string(f), string(FormatCSVGZ), string(FormatMMDB)),
	}
}

// The wire shapes, re-exported so a consumer never has to name an internal
// package.
type (
	// Database is one database FAMILY, with your organization's license beside
	// it. LicenseType, Starts and Expires are nil when there is no license.
	Database = api.Database
	// DatabaseVersion is one published version of a family. Its ID is what the
	// download, checksum and metadata calls take. Old versions are frozen
	// rather than migrated, so both stay downloadable.
	DatabaseVersion = api.DatabaseVersion
	// DatabaseMetadata is the build document the exporter writes, served
	// through unchanged.
	DatabaseMetadata = api.DatabaseMetadata
	// DatabaseMetadataColumn is one column of one format's schema.
	DatabaseMetadataColumn = api.DatabaseMetadataColumn
	// DownloadAttempt is one entry of the download history, refusals included.
	// Bytes is the object size at redirect time rather than bytes delivered:
	// the transfer runs straight from object storage, so how much of it was
	// taken is not observed.
	DownloadAttempt = api.Download
	// DownloadOutcome is how one download attempt ended.
	DownloadOutcome = api.DownloadOutcome
	// LicenseType is what a license permits you to do with the data.
	LicenseType = api.DatabaseLicenseType
	// Standing is where a license stands: live, lapsed, or never bought.
	Standing = api.Standing
	// Date is a calendar date with no time of day, as DatabaseMetadata.Updated
	// carries.
	Date = types.Date
)

// Standing tells you whether a database is yours today, was, or has never been
// bought.
const (
	StandingLicensed   = api.StandingLicensed
	StandingExpired    = api.StandingExpired
	StandingUnlicensed = api.StandingUnlicensed
)

// LicenseType is nil rather than one of these when there is no license at
// all, so read the pointer before comparing it.
const (
	LicenseTypeEvaluation   = api.Evaluation
	LicenseTypeStandard     = api.Standard
	LicenseTypeRedistribute = api.Redistribute
)

const (
	DownloadOutcomeOK           = api.DownloadOutcomeOk
	DownloadOutcomeUnauthorized = api.DownloadOutcomeUnauthorized
	DownloadOutcomeDenied       = api.DownloadOutcomeDenied
	DownloadOutcomeExpired      = api.DownloadOutcomeExpired
	DownloadOutcomeUnknown      = api.DownloadOutcomeUnknown
	DownloadOutcomeUnavailable  = api.DownloadOutcomeUnavailable
)
