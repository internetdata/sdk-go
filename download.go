package internetdata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/internetdata/sdk-go/internal/api"
)

// DownloadURL is the time-limited URL for one database file.
//
// The API answers 302 to object storage and the redirect is NOT followed: the
// URL is returned so a caller can decide how to transfer a file that runs to
// gigabytes, hand it to a downloader, or pass it on without passing on the API
// key. The link is presigned and so authorizes itself; it authorizes the START
// of a transfer, so one already running is not interrupted when it lapses.
func (d *DatabaseAPI) DownloadURL(ctx context.Context, id string, format Format) (string, error) {
	ctx = withoutRedirects(ctx)
	return withRetry(ctx, d.retries, func() (string, error) {
		res, err := d.api.DownloadDatabaseV2WithResponse(ctx, &api.DownloadDatabaseV2Params{
			ID:     id,
			Format: api.DownloadDatabaseV2ParamsFormat(format),
		})
		if err != nil {
			return "", errorFromTransport(err)
		}
		if res.StatusCode() != http.StatusFound {
			return "", errorFromResponse(res.StatusCode(), res.HTTPResponse.Header, res.Body)
		}
		if res.Headers302 == nil || res.Headers302.Location == nil {
			return "", &Error{
				Kind:       KindServerError,
				Message:    "redirect carried no Location header",
				StatusCode: res.StatusCode(),
			}
		}
		return *res.Headers302.Location, nil
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
func (d *DatabaseAPI) Download(
	ctx context.Context, id string, format Format, dst io.Writer,
) (int64, error) {
	res, err := d.fetchFile(ctx, id, format)
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
	res, err := d.fetchFile(ctx, id, format)
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
	res, err := d.fetchFile(ctx, id, format)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.ContentLength < 0 {
		return io.ReadAll(res.Body)
	}
	// Allocated once from the declared length. io.ReadAll grows by doubling, so
	// on a large file the final grow alone costs twice the payload. ReadFull
	// also turns the length into a check: short means the transfer was cut off.
	buf := make([]byte, res.ContentLength)
	if _, err := io.ReadFull(res.Body, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// The 302 is followed as a SECOND, unauthenticated request rather than by
// loosening the redirect guard: the presigned URL authorizes itself, so
// forwarding the API key would hand a credential to a host with no business
// holding it. The key rides a request editor on the generated client, which
// this request does not go through.
func (d *DatabaseAPI) fetchFile(
	ctx context.Context, id string, format Format,
) (*http.Response, error) {
	url, err := d.DownloadURL(ctx, id, format)
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
