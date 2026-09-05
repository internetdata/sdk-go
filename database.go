package internetdata

import (
	"context"
	"net/http"

	"github.com/oapi-codegen/runtime/types"

	"github.com/internetdata/sdk-go/internal/api"
)

// List is the published catalog, with your organization's licence beside each
// family. A licence covers a family, while a download names one of its
// versions, so the ids the download and checksum calls take come from
// Database.Versions.
//
// The listing is the SERVER's answer about YOUR key and nothing else assembles
// it. A database commissioned for a single customer is absent for every other
// organization rather than listed as unlicensed, so what you get back is not
// necessarily what another key gets back, and neither the whole catalog nor
// any part of it can be reconstructed from another source.
func (c *Client) List(ctx context.Context) ([]Database, error) {
	return withRetry(ctx, c.retries, func() ([]Database, error) {
		res, err := c.api.ListDatabasesWithResponse(ctx)
		if err != nil {
			return nil, errorFromTransport(err)
		}
		if res.StatusCode() != http.StatusOK || res.JSON200 == nil {
			return nil, errorFromResponse(res.StatusCode(), res.HTTPResponse.Header, res.Body)
		}
		return res.JSON200.Databases, nil
	})
}

// Metadata is what is inside one database: its columns per format, sample rows,
// the row count and the byte size of each file. It carries Updated and Entries
// without downloading anything, so poll it to decide whether today's build is
// worth fetching, and read Size to budget a transfer before starting one.
//
// One document describes every format the database is built in, which is why
// there is no format argument.
func (c *Client) Metadata(ctx context.Context, id string) (*DatabaseMetadata, error) {
	return withRetry(ctx, c.retries, func() (*DatabaseMetadata, error) {
		res, err := c.api.DatabaseMetadataV2WithResponse(ctx, &api.DatabaseMetadataV2Params{ID: id})
		if err != nil {
			return nil, errorFromTransport(err)
		}
		if res.StatusCode() != http.StatusOK || res.JSON200 == nil {
			return nil, errorFromResponse(res.StatusCode(), res.HTTPResponse.Header, res.Body)
		}
		return res.JSON200, nil
	})
}

// Checksums are the digests of one published file, for verifying a download.
// All four the exporter writes are returned, because which of them a caller
// wants is not this library's decision.
func (c *Client) Checksums(ctx context.Context, id string, format Format) (*Checksums, error) {
	return withRetry(ctx, c.retries, func() (*Checksums, error) {
		res, err := c.api.DatabaseChecksumV2WithResponse(ctx, &api.DatabaseChecksumV2Params{
			ID:     id,
			Format: api.DatabaseChecksumV2ParamsFormat(format),
		})
		if err != nil {
			return nil, errorFromTransport(err)
		}
		if res.StatusCode() != http.StatusOK || res.JSON200 == nil {
			return nil, errorFromResponse(res.StatusCode(), res.HTTPResponse.Header, res.Body)
		}
		// The digests hang under a `checksums` key rather than sitting at the
		// top level beside `id` and `format`.
		sums := res.JSON200.Checksums
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
func (c *Client) Downloads(ctx context.Context, limit int) ([]DownloadAttempt, error) {
	return withRetry(ctx, c.retries, func() ([]DownloadAttempt, error) {
		params := &api.ListDownloadsParams{}
		if limit > 0 {
			params.Limit = &limit
		}
		res, err := c.api.ListDownloadsWithResponse(ctx, params)
		if err != nil {
			return nil, errorFromTransport(err)
		}
		if res.StatusCode() != http.StatusOK || res.JSON200 == nil {
			return nil, errorFromResponse(res.StatusCode(), res.HTTPResponse.Header, res.Body)
		}
		return res.JSON200.Downloads, nil
	})
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

// The wire shapes, re-exported so a consumer never has to name an internal
// package.
type (
	// Database is one database FAMILY, with your organization's licence beside
	// it. Redistribution, Starts and Expires are nil when there is no licence.
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
	// Redistribution is what a licence permits you to do with the data.
	Redistribution = api.DatabaseRedistribution
	// Standing is where a licence stands: live, lapsed, or never bought.
	Standing = api.DatabaseStanding
	// Date is a calendar date with no time of day, as DatabaseMetadata.Updated
	// carries.
	Date = types.Date
)

// Standing tells you whether a database is yours today. It never says a
// database does not exist: a family built for one customer is simply absent
// from another organization's listing.
const (
	StandingLicensed   = api.DatabaseStandingLicensed
	StandingExpired    = api.DatabaseStandingExpired
	StandingUnlicensed = api.DatabaseStandingUnlicensed
)

// Redistribution is nil rather than one of these when there is no licence at
// all, so read the pointer before comparing it.
const (
	RedistributionEvaluation   = api.Evaluation
	RedistributionInternal     = api.Internal
	RedistributionRedistribute = api.Redistribute
)

const (
	DownloadOutcomeOK           = api.DownloadOutcomeOk
	DownloadOutcomeUnauthorized = api.DownloadOutcomeUnauthorized
	DownloadOutcomeDenied       = api.DownloadOutcomeDenied
	DownloadOutcomeExpired      = api.DownloadOutcomeExpired
	DownloadOutcomeUnknown      = api.DownloadOutcomeUnknown
	DownloadOutcomeUnavailable  = api.DownloadOutcomeUnavailable
)
