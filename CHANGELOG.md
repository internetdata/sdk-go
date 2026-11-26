# Changelog

What each release changed for you, newest first. Each line is a commit's summary, linked to its full description and diff. Releases before 2.3.1 are described by their release commits.

## 2.18.1 - 2026-11-26

### Fixes

- Refuse a user agent on the AS and domain lookups ([`d5f2d6b`](https://github.com/internetdata/sdk-go/commit/d5f2d6bdfb9251aefc56fe9bc698bb4eb2e4eae9))

## 2.18.0 - 2026-11-25

### Features

- Send a visitor's user agent with a lookup or a batch ([`bab6b77`](https://github.com/internetdata/sdk-go/commit/bab6b77b0705fdacae30a01d5816d90fecf8af27))
- Middleware: AllowBots lets a verified crawler through ([`33117c9`](https://github.com/internetdata/sdk-go/commit/33117c98b13c966225b89abc2eaf098db2c4f25e))

### Fixes

- Re-pin the spec to 2026.11.25: a paid field's description names a paid plan or the trial ([`1f12f60`](https://github.com/internetdata/sdk-go/commit/1f12f60130c06b62d96f1220f62916bc4756e59e))

## 2.17.0 - 2026-11-24

### Features

- Match a condition on a list member when any element matches ([`f08af5f`](https://github.com/internetdata/sdk-go/commit/f08af5ff9f5b882384fbb964a0151b1d6ad897e8))

### Fixes

- Hold every first_seen and last_seen to a full date, on spec 2026.11.23 ([`af8cc98`](https://github.com/internetdata/sdk-go/commit/af8cc9809f411ae84e18c76ac0b9b885f5719c3c))

## 2.16.2 - 2026-11-23

### Fixes

- Comments: whose bot an unverified address claims, on spec 2026.11.22 ([`b713679`](https://github.com/internetdata/sdk-go/commit/b7136797eb0c3665b2bcd64c2bf76d17364a3cf9))

## 2.16.1 - 2026-11-23

### Fixes

- Refuse an AS answered without its country or RPKI status ([`1876486`](https://github.com/internetdata/sdk-go/commit/1876486895bb7000782b38403e07d2f1dc7183a4))
- Tests: a value added to an answer's vocabulary is handed back ([`fc0fe15`](https://github.com/internetdata/sdk-go/commit/fc0fe15cae0f40925096b8a061c44f4925fb2626))

## 2.16.0 - 2026-11-22

### Features

- Read is_bot and the bot row, on spec 2026.11.21 ([`26342f7`](https://github.com/internetdata/sdk-go/commit/26342f7c3a5744d7d9d8d2487d88afd79e67183e))
- README: the bot flag on every plan, and whose bot it is on a paid plan ([`b8c3661`](https://github.com/internetdata/sdk-go/commit/b8c366166d70d0025615ba272c4a9b913030468e))

### Fixes

- Comments: the free plan counts its trial by the UTC month ([`fe1a949`](https://github.com/internetdata/sdk-go/commit/fe1a9491ec1d29abbbcbd7ed4fe7b851c0b432f9))

## 2.15.0 - 2026-11-20

### Features

- Read a paid domain answer's hosting, co-hosted domains, home page, redirects and backlinks ([`b834503`](https://github.com/internetdata/sdk-go/commit/b834503addddb4cf40d804548458c9b118a69929))

### Fixes

- README: what a paid domain answer adds, and the free plan's usage without a period ([`86f4d1d`](https://github.com/internetdata/sdk-go/commit/86f4d1daf9c3bc1226b0e8f142f698e8de0be072))

## 2.14.3 - 2026-11-20

### Fixes

- Re-pin the spec to 2026.11.20: the trial without a count, and no per-minute limit ([`405b3fc`](https://github.com/internetdata/sdk-go/commit/405b3fcc1fce19cdbfb7faf1ffd216abebef1b5f))

## 2.14.2 - 2026-11-19

### Fixes

- Cache a spent trial's or a reached cap's answer for a minute at most ([`0f62fa3`](https://github.com/internetdata/sdk-go/commit/0f62fa3709f1943e68426f3d08f8424992c4e9bf))

## 2.14.1 - 2026-11-18

### Fixes

- Re-pin the spec to 2026.11.17 as republished: KEY_OVER_HTTP on the account 403 ([`d46f13c`](https://github.com/internetdata/sdk-go/commit/d46f13c54aae80e0fc3af516aa06527896d69238))

## 2.14.0 - 2026-11-17

### Features

- Read cap_reached on a paid answer past its overage cap ([`d6e527c`](https://github.com/internetdata/sdk-go/commit/d6e527c9b9cb7552cc383297f1993d3518db342f))

## 2.13.0 - 2026-11-16

### Features

- Read an AS's rank, neighbors and hosted domains, and the plan's name ([`a81ee6e`](https://github.com/internetdata/sdk-go/commit/a81ee6ec71d69b963c37820527ecbdb1cb5336d5))

## 2.12.0 - 2026-11-15

### Features

- Read hosted_domains' 10 most popular domains ([`480d856`](https://github.com/internetdata/sdk-go/commit/480d856e7378c310720ad756b36721818d708b3f))

## 2.11.1 - 2026-11-14

### Fixes

- Re-pin the spec to 2026.11.13 as republished: the ip.lookup scope 403 ([`884022b`](https://github.com/internetdata/sdk-go/commit/884022b279fd969957183021d6770362d578f927))

## 2.11.0 - 2026-11-14

### Features

- Add the framework middleware core ([`9c786f0`](https://github.com/internetdata/sdk-go/commit/9c786f09f88175aaedc7075a10d7c790bbfc1e49))

## 2.10.0 - 2026-11-13

### Features

- Look up an address, an AS or a domain: Lookup, LookupBatch, LookupASN and LookupDomain ([`18b3b9b`](https://github.com/internetdata/sdk-go/commit/18b3b9b8e776f5edbfd717dd83eb80b7afe2d32c))
- Default to https://api.internetdata.io, and sign in at the apex ([`d782bf6`](https://github.com/internetdata/sdk-go/commit/d782bf69f48598dfdbdd7079f59533784d0b8bcc))
- Name the SDK in every request's User-Agent ([`922a314`](https://github.com/internetdata/sdk-go/commit/922a314a01c5afe52ede424b8c6bb6ba4d4449f4))

### Fixes

- Re-pin the spec to 2026.11.13: the lookups ([`f3e3978`](https://github.com/internetdata/sdk-go/commit/f3e3978cb46e614667d4e99e65d772505cfa475d))

## 2.9.1 - 2026-11-09

### Fixes

- Re-pin the spec to 2026.11.08: the token and revoke descriptions ([`602ac69`](https://github.com/internetdata/sdk-go/commit/602ac699f89e58cee37d8c3b23c59a9fe14ceb41))
- Restore v2.8's download signatures, and ask for a build with AtBuild ([`b01390f`](https://github.com/internetdata/sdk-go/commit/b01390fee8a2c02ff7315856d57863999db3d234))

## 2.9.0 - 2026-11-07

### Features

- Download a named build: DownloadURL, Download, DownloadFile and DownloadBytes take Build ([`d9b936e`](https://github.com/internetdata/sdk-go/commit/d9b936ea6a52922dbc7b4c6e64f3c78c21d5fd5d))

## 2.8.6 - 2026-11-06

### Fixes

- Re-pin the spec to 2026.11.05: the download's build parameter ([`f5caa1c`](https://github.com/internetdata/sdk-go/commit/f5caa1c79a87cfb35db690a0869e879aabea1419))

## 2.8.5 - 2026-11-03

### Fixes

- Re-pin the spec to 2026.11.02: ID and IDs in its descriptions ([`8aa3d4c`](https://github.com/internetdata/sdk-go/commit/8aa3d4cbb8b168d5c5a124e86d1d23ba08933613))
- Write ID and IDs in prose, never id or ids ([`6f00318`](https://github.com/internetdata/sdk-go/commit/6f003183635fbe18fa7f3275bf0f5d26fa850645))
- Refuse an OAuth answer whose required member is null ([`2bb99ea`](https://github.com/internetdata/sdk-go/commit/2bb99ead571e78e28055c05353c5f8708b1464ed))

## 2.8.4 - 2026-10-30

### Fixes

- Read a date-time in RFC 3339's form alone, its T and Z in either case ([`e1dd7dd`](https://github.com/internetdata/sdk-go/commit/e1dd7ddaae3394fd2654ac80b275199a81b30cc1))

## 2.8.3 - 2026-10-22

### Fixes

- Read a Retry-After date and a date-time only in the zones they allow ([`8f18778`](https://github.com/internetdata/sdk-go/commit/8f18778ef4d932367f4682401fb38daa960ad718))

## 2.8.2 - 2026-10-21

### Fixes

- Re-pin the spec to 2026.10.21: a 403 for a refused address ([`8d2b165`](https://github.com/internetdata/sdk-go/commit/8d2b165326d9f156153b0c5d25645758c0aa7f32))

## 2.8.1 - 2026-10-18

### Fixes

- Re-pin the spec to 2026.10.18: client_id_issued_at is int64 ([`1ed5779`](https://github.com/internetdata/sdk-go/commit/1ed577905ad0dd04776b53c983ea1db5f31c0f0f))

## 2.8.0 - 2026-10-18

### Features

- Add Oauth.Register, for RFC 7591 dynamic client registration ([`34b696d`](https://github.com/internetdata/sdk-go/commit/34b696d78f37436c977370bbb0121dff1fcba11a))

## 2.7.0 - 2026-10-17

### Features

- Re-pin the spec to 2026.10.16: dynamic client registration ([`476d112`](https://github.com/internetdata/sdk-go/commit/476d11246e1b5bca91d297f93f870d6546c50284))

## 2.6.5 - 2026-10-16

### Fixes

- Re-pin the spec to 2026.10.15: three descriptions in US spelling ([`b3431fb`](https://github.com/internetdata/sdk-go/commit/b3431fb8293bd0bad71a9476405258f28a124a9f))

## 2.6.4 - 2026-10-13

### Fixes

- Re-pin the spec to 2026.10.13: Open databases come with a license ([`f4fbee3`](https://github.com/internetdata/sdk-go/commit/f4fbee3aab990ce2bb556237473ed23342e5e05f))

## 2.6.3 - 2026-10-12

### Fixes

- Trim the API key, and refuse one holding a control character ([`e0ad659`](https://github.com/internetdata/sdk-go/commit/e0ad6596e175826a2ae8baa6da01b6cff107889e))

## 2.6.2 - 2026-10-10

### Fixes

- Re-pin the spec to 2026.10.09: rotating a key needs apikeys.reveal ([`a89a9ee`](https://github.com/internetdata/sdk-go/commit/a89a9ee86c0514397b6b38c079ac92efd43539d4))

## 2.6.1 - 2026-10-09

### Fixes

- Retry an answer a database call cannot read, as a server error with its status ([`590369e`](https://github.com/internetdata/sdk-go/commit/590369e67363a729b771bab364a3305592b45663))
- Read a Retry-After as digits or an HTTP date, and nothing else ([`7d20e4a`](https://github.com/internetdata/sdk-go/commit/7d20e4a7efe51f59adcaa1c0d24e767d6f5de9da))

## 2.6.0 - 2026-10-09

### Features

- Re-pin the spec to 2026.10.08, adding the Open databases' open flag ([`bb04173`](https://github.com/internetdata/sdk-go/commit/bb0417333177fbffa5e73dc42e29f345c8d58496))

## 2.5.1 - 2026-10-04

### Fixes

- Re-pin the spec to 2026.10.03: metadata needs no license ([`82af7fa`](https://github.com/internetdata/sdk-go/commit/82af7fa4cc7ecdc1468e46dcc1a811e708069504))

## 2.5.0 - 2026-09-29

### Features

- Add the authorization code sign-in, with PKCE ([`948f6e3`](https://github.com/internetdata/sdk-go/commit/948f6e315d5858f3b51de553c56b0da931f4e6d4))

## 2.4.1 - 2026-09-28

### Fixes

- Compile on 32-bit platforms again ([`a20cc44`](https://github.com/internetdata/sdk-go/commit/a20cc440bdb835c21da6f65466819415b34c3d6c))

## 2.4.0 - 2026-09-27

### Features

- Re-pin the spec to 2026.09.26, adding its evaluation-sample fields ([`25cd942`](https://github.com/internetdata/sdk-go/commit/25cd9422b6508f93bcedda812316ad525c71fa1a))

### Fixes

- Drop every trailing slash, and bound an overlong Retry-After ([`f0603b0`](https://github.com/internetdata/sdk-go/commit/f0603b084c8db0b1f40f4c9d44a97c64025e81ef))
- Fail DownloadBytes on a length no process can hold, never panic ([`8037284`](https://github.com/internetdata/sdk-go/commit/8037284ae945f28013bf506cba4ae90e443a0cf7))
- End the poll's sleep at its deadline, and saturate a server's values ([`31be056`](https://github.com/internetdata/sdk-go/commit/31be056b047d044562e0cd8456e5f928a5493d1a))

## 2.3.1 - 2026-09-22

### Fixes

- Stop explaining in the docs how a private database is hidden ([`74a1d2e`](https://github.com/internetdata/sdk-go/commit/74a1d2e3f78cb0a2f3956ea667c8d5c57ce7c45b))
