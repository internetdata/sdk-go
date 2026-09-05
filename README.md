# [<img src="https://s3.internetdata.io/internetdata-public/brand/mark.svg" alt="InternetData" height="28"/>](https://internetdata.io/) InternetData Go Client Library

[![Go Reference](https://pkg.go.dev/badge/github.com/internetdata/sdk-go.svg)](https://pkg.go.dev/github.com/internetdata/sdk-go)
[![license](https://img.shields.io/github/license/internetdata/sdk-go)](LICENSE)

The official Go client library for the [InternetData](https://internetdata.io) API.

The library downloads the IP and network databases your organization is licensed for, in CSV.GZ or MMDB, and tells you what is inside each one before you fetch it.

## Getting Started

```bash
go get github.com/internetdata/sdk-go
```

Requires Go 1.24 or newer. The module path ends in `sdk-go`, but the package it declares is `internetdata`:

```go
import internetdata "github.com/internetdata/sdk-go"
```

## Usage

Every database published today is licensed, so start with an API key carrying the `db.download` scope. Create one in the [console](https://app.internetdata.io) and pass it to `New` as an option:

```go
client, err := internetdata.New(internetdata.WithAPIKey(os.Getenv("INTERNETDATA_API_KEY")))
if err != nil {
    log.Fatal(err)
}

databases, err := client.Database.List(ctx)
for _, db := range databases {
    fmt.Println(db.Base, db.Standing)   // bogon_ip licensed
}
```

Every call hangs off `client.Database`, which is the whole of this API and is where the sibling VPNDetection library keeps the same seven calls.

### The catalog

`List` returns one entry per database FAMILY, with your licence beside it. A licence covers the family, while a download names one of its versions, so the ids you pass to everything else come from `Versions`:

```go
for _, db := range databases {
    if db.Standing != internetdata.StandingLicensed {
        continue
    }
    for _, v := range db.Versions {
        fmt.Println(v.ID, v.Formats)    // vpn_ip_v1 [csvgz mmdb]
    }
}
```

`Standing` is `licensed`, `expired` or `unlicensed`, and `Redistribution` is what your licence permits: `evaluation`, `internal`, `redistribute`, or `nil` when there is no licence at all. Old versions are frozen rather than migrated, so both stay downloadable.

### What is inside one, before you fetch it

`Metadata` carries the row count, the build date, the columns of each format and the byte size of each file, without downloading anything. Poll it to decide whether today's build is worth fetching, and read `Size` to budget a transfer before you start one:

```go
meta, err := client.Database.Metadata(ctx, "vpn_ip_v1")
fmt.Println(meta.Updated, meta.Entries, meta.Size["mmdb"])

for _, column := range meta.Schema["csvgz"] {
    fmt.Println(column.Name, column.Type)
}
```

### Downloading

`DownloadFile` writes one file to a path, streaming it straight to disk so that nothing bigger than a chunk is ever held in memory:

```go
written, err := client.Database.DownloadFile(ctx, "vpn_ip_v1", internetdata.FormatMMDB, "./vpn_ip_v1.mmdb")
fmt.Printf("%d bytes\n", written)
```

The bytes land in a neighboring `.part` file that is renamed on completion, so a transfer that dies half way leaves no truncated file that reads as a whole database, and a failed refresh cannot destroy the copy already there.

Or stream it into a writer of your own, or take a small one as bytes:

```go
written, err := client.Database.Download(ctx, "vpn_ip_v1", internetdata.FormatMMDB, w)
raw, err := client.Database.DownloadBytes(ctx, "bogon_ip_v1", internetdata.FormatCSVGZ)
```

`DownloadBytes` holds the whole file in memory, and the catalog spans five orders of magnitude, so use `DownloadFile` for anything you have not measured against `Metadata`'s `Size`.

### Downloading it yourself

The API answers a download with a redirect to a time-limited URL on object storage. `DownloadURL` hands you that URL rather than following it, so you can pass it to a downloader, a job queue or another machine:

```go
url, err := client.Database.DownloadURL(ctx, "vpn_ip_v1", internetdata.FormatMMDB)
```

The link is presigned and so authorizes itself; it carries no API key of yours, and the library never sends your key to object storage. It authorizes the START of a transfer, so one already running is not interrupted when the link lapses.

### Verifying a download

`Checksums` returns all four digests the exporter publishes for one file:

```go
sums, err := client.Database.Checksums(ctx, "vpn_ip_v1", internetdata.FormatMMDB)
fmt.Println(sums.SHA256)
```

### Download history

`Downloads` is your organization's recent attempts, newest first, refusals included: a denial is what answers "it stopped working", and its absence answers nothing.

```go
history, err := client.Database.Downloads(ctx, 50)
for _, attempt := range history {
    fmt.Println(attempt.Created, attempt.DatasetID, attempt.Outcome)
}
```

### Errors

Failures return an `*internetdata.Error` carrying a `Kind`, the API's own result code, and a `Retryable` flag:

```go
_, err := client.Database.DownloadURL(ctx, "vpn_ip_v1", internetdata.FormatMMDB)
var apiErr *internetdata.Error
if errors.As(err, &apiErr) {
    fmt.Println(apiErr.Kind, apiErr.Message, apiErr.Retryable())
}
```

`Kind` is one of `bad_request`, `unauthorized`, `forbidden`, `rate_limited`, `quota_exceeded`, `server_error` or `network`. `Message` is the API's `rc`, which is what separates two refusals that share a status: `NOT_LICENSED` means buy it and `LICENSE_EXPIRED` means renew it, and both are `403`.

Note that `rate_limited` and `quota_exceeded` both arrive as HTTP 429 and are not the same thing. A rate limit is when the API faces extreme traffic bursts and so retrying later works; but a spent quota needs your allowance raised or the window to roll over. The library retries rate limits for you, but not if your quota is exceeded.

### Your catalog is yours

## Other Libraries

There are official InternetData client libraries available for many languages including PHP, Python, Go, Java, Ruby, and many popular frameworks such as Django, Rails, and Laravel. See our GitHub at https://github.com/internetdata for more.

## About InternetData

IP, ASN and Domain data to reveal unique insights about the internet. APIs, Databases and Live Feeds available.

[<img src="https://s3.internetdata.io/internetdata-public/brand/mark.svg" alt="InternetData" width="96"/>](https://internetdata.io/)

## License

This project is licensed under the [MIT License](LICENSE).
