# Changelog

What each release changed for you, newest first. Each line is a commit's summary, linked to its full description and diff. Releases before 2.3.1 are described by their release commits.

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
