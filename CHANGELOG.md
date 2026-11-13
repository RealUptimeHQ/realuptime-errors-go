# Changelog

## 0.1.1 (2026-11-13)


### Bug Fixes

* **errors:** scrub credential values in name=value text and query strings (REA-1260)

## 0.1.0 (2026-11-12)

First tagged release. The module was usable from the repository before it carried a version tag.

### Features

* Error capture with bounded buffering and honest drop counters, standard library only.
* Scrub-by-default for sensitive values before an event leaves the process, including the key-name denylist and the AWS access key, Stripe webhook secret and URL userinfo patterns.
* HTTP middleware and a `log/slog` handler.
* Scrub rules shared with the other SDKs through vendored test vectors, so behavior stays identical across languages.
