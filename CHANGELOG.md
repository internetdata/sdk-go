# Changelog

What each release changed for you, newest first. Each line is a commit's summary, linked to its full description and diff. Releases before 2.3.1 are described by their release commits.

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
