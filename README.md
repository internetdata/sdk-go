# [<img src="https://s3.internetdata.io/internetdata-public/brand/mark.svg" alt="InternetData" height="28"/>](https://internetdata.io/) InternetData Go Client Library

[![Go Reference](https://pkg.go.dev/badge/github.com/internetdata/sdk-go/v2.svg)](https://pkg.go.dev/github.com/internetdata/sdk-go/v2)
[![license](https://img.shields.io/github/license/internetdata/sdk-go)](LICENSE)

The official Go client library for the [InternetData](https://internetdata.io) API.

It tells you what InternetData's databases say about an IP address, an AS or a domain, and downloads the databases your organization is licensed for, in CSV.GZ or MMDB.

## Getting Started

```bash
go get github.com/internetdata/sdk-go/v2
```

Requires Go 1.26 or newer. The module path ends in `sdk-go`, but the package it declares is `internetdata`:

```go
import internetdata "github.com/internetdata/sdk-go/v2"
```

## Usage

**No API key needed to start.** Unlimited, the free plan, answers where an address is, the AS and company behind it, its abuse contact, and whether it's a Tor exit, a relay, anycast, a carrier or satellite network, or runs SSH, SMTP, a web server, a DNS resolver or BitTorrent, limited per address per minute.

```go
client, err := internetdata.New()
if err != nil {
    log.Fatal(err)
}

result, err := client.Lookup(ctx, "146.70.116.130")
if err != nil {
    log.Fatal(err)
}
fmt.Println(result.Location.Country)   // AT
fmt.Println(result.Asn.Name)           // M247 Europe SRL
fmt.Println(result.IsTor)              // false
```

### With an API key

A paid plan answers every field: `is_vpn`, `is_hosting`, `is_cdn` and the four proxy flags as well, the provider and confidence behind each flag, the location down to the city, and the whois record. A free account tries every field, 100 lookups a day. Create a key in the [console](https://app.internetdata.io), then pass it in:

```go
client, err := internetdata.New(internetdata.WithAPIKey(os.Getenv("INTERNETDATA_API_KEY")))

result, err := client.Lookup(ctx, "146.70.116.130")
fmt.Println(internetdata.BoolValue(result.IsVpn))   // true, on a paid plan

if vpn := result.Vpn; vpn != nil {
    fmt.Println(vpn.ProviderName)       // Mullvad, on a paid plan
}
```

### An AS or a domain

```go
asn, err := client.LookupASN(ctx, 9009)
fmt.Println(asn.Name, asn.Country)     // M247 Europe SRL RO

domain, err := client.LookupDomain(ctx, "example.com")
fmt.Println(domain.IsRegistered)       // true
```

Every plan answers an AS's registration and whether a domain is registered and which record types it has. A paid plan adds the AS's whois record and its prefixes, and the domain's records, IP ranges and `is_disposable`. Neither is cached or batched, and `Lookup` takes an address alone: it refuses anything else as a `bad_request` before sending it.

### Your own address

```go
result, err := client.MyIP(ctx)
fmt.Println(result.IP)   // the address we saw this call come from
```

Same answer `Lookup` would give for that address, and the same cost against your allowance. It's deliberately not cached: which address you are is the whole question, and a machine that moves between networks would otherwise be told where it used to be.

### Your plan and usage

```go
acct, err := client.MyEntitlement(ctx)
fmt.Println(acct.Plan.Key)         // max
fmt.Println(acct.Usage.Requests)   // 580
fmt.Println(acct.Usage.WindowEnd)  // when the allowance resets
```

Usage is counted against the anniversary of your subscription on a paid plan, not the calendar month and not the billing period, and against the UTC day on the free plan; it's the same number a lookup is gated on. `HardLimit` is nil on an uncapped plan, which is not the same as zero.

### Batch lookup

Look up many addresses at once. Bogons and cached answers are handled locally, and everything else goes to the batch endpoint in chunks of up to 1000 addresses, in parallel. The batch endpoint needs a paid plan:

```go
results, err := client.LookupBatch(ctx, []string{"146.70.116.130", "8.8.8.8", "1.1.1.1"})

for ip, result := range results {
    if result.Err != nil {
        fmt.Printf("%s: %v\n", ip, result.Err)
        continue
    }
    fmt.Printf("%s: %s\n", ip, result.Result.Location.Country)
}
```

Results are keyed by address, so duplicates in your list collapse into a single entry and one address failing never loses the rest: it carries its error as its value, with the status the API would have given that address on its own.

Pass as many addresses as you like; splitting them into chunks is the library's job. How many chunks are in flight at once, how many times a failed chunk is retried, and how long each attempt may take are configurable per call:

```go
results, err := client.LookupBatch(ctx, manyIPs,
    internetdata.Concurrency(4), internetdata.Retries(4), internetdata.Timeout(10*time.Second))
```

`Retries` and `Timeout` work on `Lookup`, `LookupASN`, `LookupDomain`, `MyIP` and `MyEntitlement` too. The timeout bounds each attempt rather than the whole call, and defaults to the HTTP client's: 30 seconds unless `WithHTTPClient` supplied another.

Every request names the SDK in its `User-Agent`, `internetdata-go/<version>`, object storage's included. A program built on the SDK can put its own name first in the transport it passes to `WithHTTPClient`, which sees that header already set.

### Caching

Address lookups are cached by default, so repeat lookups of the same address are free:

```go
client, err := internetdata.New()

result, err := client.Lookup(ctx, "146.70.116.130")
fmt.Println(result.Asn.Asn)    // 9009, API request

result2, err := client.Lookup(ctx, "146.70.116.130")
fmt.Println(result2.Asn.Asn)   // 9009, no API request, result was cached
```

You can change the default cache variables (max size, TTL, etc) on initialization, or even disable it:

```go
client, err := internetdata.New(internetdata.WithCache(50_000, 6*time.Hour))
clientNoCache, err := internetdata.New(internetdata.WithoutCache())
```

### Private and reserved addresses

Private, loopback, link-local, documentation and multicast addresses (and their IPv6 equivalents, including the 6to4 and Teredo ranges) are answered locally, every flag false and every field empty, so they cost no request and no quota:

```go
result, err := client.Lookup(ctx, "192.168.1.1")
result.IsBogon   // true, this answer was computed rather than served
result.IsTor     // false
```

Because the answer is computed rather than served, it always carries every field, whatever plan you are on. Do not read your plan's shape off a private address.

The check is available on the client, which is handy when your inputs are addresses anyway:

```go
client.IsBogon("10.0.0.1")   // true
client.IsBogon("8.8.8.8")    // false
```

It is also callable on its own, if you want it without a client:

```go
internetdata.IsBogon("10.0.0.1")   // true
```

### Errors

Failures return an `*internetdata.Error` carrying a `Kind`, the API's own message, and a `Retryable` flag:

```go
result, err := client.Lookup(ctx, "146.70.116.130")
var apiErr *internetdata.Error
if errors.As(err, &apiErr) {
    fmt.Println(apiErr.Kind, apiErr.Message, apiErr.Retryable())
}
```

`Kind` is one of `bad_request`, `unauthorized`, `forbidden`, `rate_limited`, `quota_exceeded`, `server_error` or `network`. A lookup's `Message` is the API's explanation. A database call's is its `rc`, which is what separates two refusals that share a status: `NOT_LICENSED` means buy it, `LICENSE_EXPIRED` means renew it, and `IP_NOT_ALLOWED` means the call came from an address the key or its organization does not allow, and all three are `403`.

Note that `rate_limited` and `quota_exceeded` both arrive as HTTP 429 and are not the same thing. A rate limit carries `Retry-After`, and retrying later works; a spent quota needs your allowance raised or the window to roll over. The library retries rate limits for you, but not if your quota is exceeded.

### Databases

The databases your organization is licensed for are under `client.Database`, for a key carrying the `db.download` scope. Every database published today is licensed:

```go
client, err := internetdata.New(internetdata.WithAPIKey(os.Getenv("INTERNETDATA_API_KEY")))

databases, err := client.Database.List(ctx)
for _, db := range databases {
    fmt.Println(db.Base, db.Standing)   // bogon_ip licensed
}
```

Every database call hangs off `client.Database`, which is where the sibling VPNDetection library keeps the same seven calls.

### The catalog

`List` returns one entry per database FAMILY, with your license beside it. A license covers the family, while a download names one of its versions, so the IDs you pass to everything else come from `Versions`:

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

`Standing` is `licensed`, `expired` or `unlicensed`, and `LicenseType` is what your license permits: `evaluation`, `standard`, `redistribute`, or `nil` when there is no license at all. Old versions are frozen rather than migrated, so both stay downloadable.

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

`Format` is a defined string type, so `Format("zip")` compiles. Anything the API does not publish is refused before the request leaves, as a `bad_request` naming what is allowed; `Valid` is the same check for a format you take from a flag or a config file:

```go
if !internetdata.Format(fromFlag).Valid() {
    // csvgz or mmdb
}
```

### Downloading a named build

Each download call fetches the build that is live unless you name one. A `database.updated` webhook names the build it announces and carries its `sha256`, so a receiver that downloads after the next publish asks for that build to get the file the hash is of. `client.Database.AtBuild(build)` has the same four download calls, for that build:

```go
written, err := client.Database.AtBuild(event.Data.Build).DownloadFile(ctx,
    "vpn_ip_v1", internetdata.FormatMMDB, "./vpn_ip_v1.mmdb")
```

A build is kept 7 days. Past that the call fails as `bad_request` with the message `BUILD_GONE`, and `client.Database`'s own download calls fetch the live one.

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

### Sign in with OAuth (device flow)

A program running on the person's own machine can let them sign in with a browser and pick one of their API keys, instead of asking them to paste it:

```go
client, err := internetdata.New()
device, err := client.Oauth.DeviceAuthorization(ctx, "your-client-id",
    internetdata.DeviceAuthorizationOptions{Scope: "account.read apikeys.read apikeys.reveal"})
fmt.Printf("Open %s and enter %s\n", device.VerificationURI, device.UserCode)

token, err := client.Oauth.PollDeviceToken(ctx, "your-client-id", device)
if token.Apikey == nil {
    log.Fatal("no API key came back: none was picked, or it cannot be revealed")
}
keyed, err := internetdata.New(internetdata.WithAPIKey(*token.Apikey))
```

A denied sign-in satisfies `errors.Is(err, internetdata.ErrOauthAccessDenied)` and a code that ran out `errors.Is(err, internetdata.ErrOauthExpiredToken)`, and client IDs are issued on request from support@internetdata.io. `client.Oauth.Revoke(ctx, "your-client-id", *token.RefreshToken)` signs the machine out again.

### Sign in with OAuth (authorization code)

An app that can take a browser redirect signs the person in there instead, with a PKCE pair made for that one sign-in:

```go
client, err := internetdata.New()
redirectURI := "http://127.0.0.1:8765/callback"
pkce := client.Oauth.CreatePkce()

authURL, err := client.Oauth.AuthorizationURL("your-client-id", redirectURI, pkce.Challenge,
    internetdata.AuthorizationURLOptions{Scope: "apikeys.use", State: "your-state"})
// Open authURL in the browser. Its redirect to redirectURI carries code and state.
token, err := client.Oauth.ExchangeAuthorizationCode(ctx, "your-client-id", code, pkce.Verifier, redirectURI)
```

Check that `state` came back as you sent it before you exchange `code`, which works once. The client ID can also be the https URL of a client metadata document your app serves, and such an app is never handed a key, so `token.Apikey` stays nil.

### Register a client

An app with no client ID, and no client metadata document to use as one, registers itself, then signs in with the `ClientID` it gets back as the client ID for `AuthorizationURL` and `ExchangeAuthorizationCode`:

```go
client, err := internetdata.New()
registration, err := client.Oauth.Register(ctx, []string{"http://127.0.0.1:8765/callback"},
    internetdata.RegisterOptions{ClientName: "Your App"})
```

Each redirect URI must be https on a public host name, or http on a loopback address, which then matches any port. Nothing is kept until a person approves the client, so an app with no client ID saved can simply register again.

### Absent is not false

Beyond the members every plan answers, each field is a pointer, because your key decides which of them the API sends, and so is each paid field inside a shared object, such as `Location.City` or `Abuse.Phone`. Unlimited leaves out `IsVpn`, `IsHosting`, `IsCdn` and the four proxy flags, the detail behind each flag but Tor's and relay's, and the whois record; a free-plan key's answer carries `TrialExpired`, true once its daily trial is spent. A `nil` pointer means "not in your plan", not "checked, and no".

```go
internetdata.BoolValue(result.IsVpn)        // when you only want the flag
result.IsVpn == nil                         // on Unlimited: not in your plan
internetdata.BoolValue(result.TrialExpired) // a free-plan key's daily trial is spent
```

## Other Libraries

There are official InternetData client libraries available for many languages including PHP, Python, Go, Java, and Ruby. See our GitHub at https://github.com/internetdata for more.

## About InternetData

Geolocation, anonymity, ownership and network databases for IP addresses and AS numbers, licensed as files you download and query yourself.

[<img src="https://s3.internetdata.io/internetdata-public/brand/mark.svg" alt="InternetData" height="64"/>](https://internetdata.io/)

## License

This project is licensed under the [MIT License](LICENSE).
