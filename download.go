package internetdata

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/internetdata/sdk-go/v2/internal/api"
)

// DownloadURL is the time-limited URL for one database file.
//
// The API answers 302 to object storage and the redirect is NOT followed: the
// URL is returned so a caller can decide how to transfer a file that runs to
// gigabytes, hand it to a downloader, or pass it on without passing on the API
// key. The link is presigned and so authorizes itself; it authorizes the START
// of a transfer, so one already running is not interrupted when it lapses.
func (d *DatabaseAPI) DownloadURL(ctx context.Context, id string, format Format) (string, error) {
	return d.downloadURL(ctx, id, format, "")
}

func (d *DatabaseAPI) downloadURL(
	ctx context.Context, id string, format Format, build string,
) (string, error) {
	if err := checkFormat(format); err != nil {
		return "", err
	}
	params := &api.DownloadDatabaseV2Params{ID: id, Format: api.DatabaseFormat(format)}
	if build != "" {
		params.Build = &build
	}
	ctx = withoutRedirects(ctx)
	return withRetry(ctx, d.retries, func() (string, error) {
		res, err := d.api.DownloadDatabaseV2(ctx, params)
		if err != nil {
			return "", errorFromTransport(err)
		}
		body, err := readBody(res)
		if err != nil {
			return "", err
		}
		switch {
		case res.StatusCode == http.StatusFound:
			if location := res.Header.Get("Location"); location != "" {
				return location, nil
			}
			return "", unreadable(res.StatusCode, "redirect carried no Location header")
		// The link answering in place of its redirect is something in the way,
		// not the caller's mistake, so it is retried; through v2.6.0 it was a
		// bad_request sent once.
		case res.StatusCode >= 200 && res.StatusCode <= 299:
			return "", unreadable(res.StatusCode, "expected a redirect to object storage")
		}
		return "", errorFromResponse(res.StatusCode, res.Header, body)
	})
}

// Download streams one database file into dst and returns the bytes written.
//
// Nothing beyond a single chunk is ever held in memory, whatever the file
// weighs. The transfer takes its deadline from ctx rather than from the HTTP
// client's Timeout, which would cap the whole body: 30 seconds is a sane bound
// on a catalog read and the wrong one on a gigabyte.
//
// A failure DURING the transfer is returned as it happened rather than wrapped
// in an *Error: a reset socket and a full disk are different problems, and only
// one of them is ours.
func (d *DatabaseAPI) Download(ctx context.Context, id string, format Format, dst io.Writer) (int64, error) {
	return d.download(ctx, id, format, dst, "")
}

func (d *DatabaseAPI) download(
	ctx context.Context, id string, format Format, dst io.Writer, build string,
) (int64, error) {
	res, err := d.fetchFile(ctx, id, format, build)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	return io.Copy(dst, res.Body)
}

// DownloadFile writes one database file to path and returns the bytes written.
//
// The bytes land in a neighboring .part file that is renamed on completion, so
// a transfer that dies half way leaves no truncated file that reads as a whole
// database, and a failed refresh cannot destroy the copy already there.
// Otherwise identical to Download.
func (d *DatabaseAPI) DownloadFile(
	ctx context.Context, id string, format Format, path string,
) (int64, error) {
	return d.downloadFile(ctx, id, format, path, "")
}

func (d *DatabaseAPI) downloadFile(
	ctx context.Context, id string, format Format, path string, build string,
) (int64, error) {
	res, err := d.fetchFile(ctx, id, format, build)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	partial := path + ".part"
	file, err := os.Create(partial)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(file, res.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(partial, path)
	}
	if err != nil {
		_ = os.Remove(partial)
		return written, err
	}
	return written, nil
}

// DownloadBytes downloads one database file and hands back its bytes.
//
// This holds the ENTIRE file in memory, and the catalog spans five orders of
// magnitude, from bogon_asn_v1 at a few hundred bytes to the largest IP feeds
// at several gigabytes. Metadata publishes a Size per format; read it first, or
// use Download or DownloadFile for anything you have not measured.
func (d *DatabaseAPI) DownloadBytes(ctx context.Context, id string, format Format) ([]byte, error) {
	return d.downloadBytes(ctx, id, format, "")
}

func (d *DatabaseAPI) downloadBytes(
	ctx context.Context, id string, format Format, build string,
) ([]byte, error) {
	res, err := d.fetchFile(ctx, id, format, build)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.ContentLength < 0 {
		return io.ReadAll(res.Body)
	}
	// Allocated once from the declared length, up to maxPrealloc: io.ReadAll
	// grows by doubling, so on a large file the final grow alone costs twice
	// the payload. Past it the buffer grows as bytes arrive, because the length
	// is the server's word: make([]byte, 1<<62) panicked with "makeslice: len
	// out of range" in the caller's goroutine, and a length the machine cannot
	// hold is a fatal out of memory, which nothing recovers from. Fewer bytes
	// than declared means the transfer was cut off.
	buf := bytes.NewBuffer(make([]byte, 0, min(res.ContentLength, maxPrealloc)))
	n, err := buf.ReadFrom(res.Body)
	if err != nil {
		return nil, err
	}
	if n != res.ContentLength {
		return nil, io.ErrUnexpectedEOF
	}
	return buf.Bytes(), nil
}

// The most DownloadBytes allocates up front from a declared Content-Length.
const maxPrealloc = 64 << 20

// The 302 is followed as a SECOND, unauthenticated request rather than by
// loosening the redirect guard: the presigned URL authorizes itself, so
// forwarding the API key would hand a credential to a host with no business
// holding it. The key rides a request editor on the generated client, which
// this request does not go through.
func (d *DatabaseAPI) fetchFile(
	ctx context.Context, id string, format Format, build string,
) (*http.Response, error) {
	url, err := d.downloadURL(ctx, id, format, build)
	if err != nil {
		return nil, err
	}
	return withRetry(ctx, d.retries, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, errorFromTransport(err)
		}
		res, err := d.transfer.Do(req)
		if err != nil {
			return nil, errorFromTransport(err)
		}
		if res.StatusCode != http.StatusOK {
			// Left unread: the status is what separates a lapsed link from a
			// refused one, and nothing bounds the size of an error body.
			res.Body.Close()
			failure := errorFromResponse(res.StatusCode, res.Header, nil)
			failure.Message = fmt.Sprintf(
				"object storage refused the download link with status %d", res.StatusCode)
			return nil, failure
		}
		return res, nil
	})
}

// AtBuild is the four download calls for one build of each database, by the
// name a database.updated webhook gives it ("20261105.0t53s5"), so the file is
// the one whose sha256 the webhook carries; DatabaseAPI's own fetch the build
// that is live, as does an empty name. A build is kept 7 days; past that the
// call fails as KindBadRequest with the message BUILD_GONE, sent once.
func (d *DatabaseAPI) AtBuild(name string) *BuildDownloads {
	return &BuildDownloads{db: d, build: name}
}

// BuildDownloads downloads the build AtBuild named, with the same four methods
// and signatures as DatabaseAPI.
type BuildDownloads struct {
	db    *DatabaseAPI
	build string
}

// DownloadURL is DatabaseAPI.DownloadURL for this build.
func (b *BuildDownloads) DownloadURL(ctx context.Context, id string, format Format) (string, error) {
	return b.db.downloadURL(ctx, id, format, b.build)
}

// Download is DatabaseAPI.Download for this build.
func (b *BuildDownloads) Download(
	ctx context.Context, id string, format Format, dst io.Writer,
) (int64, error) {
	return b.db.download(ctx, id, format, dst, b.build)
}

// DownloadFile is DatabaseAPI.DownloadFile for this build.
func (b *BuildDownloads) DownloadFile(
	ctx context.Context, id string, format Format, path string,
) (int64, error) {
	return b.db.downloadFile(ctx, id, format, path, b.build)
}

// DownloadBytes is DatabaseAPI.DownloadBytes for this build.
func (b *BuildDownloads) DownloadBytes(ctx context.Context, id string, format Format) ([]byte, error) {
	return b.db.downloadBytes(ctx, id, format, b.build)
}
