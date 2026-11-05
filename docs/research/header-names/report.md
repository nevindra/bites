# Header names derived from Go field names

Taken on 2026-11-01 for the revision of [ADR 0011](../../adr/0011-one-naming-policy-for-wire-names.md).

## What was run

`go run .` in this folder (go1.27.3 linux/amd64, at commit 4291b54). `main.go` holds a copy of `wire.Words` as of that commit (a probe is its own module and cannot import `internal/`), the header name it gives (`wire.HeaderName`), and the proposed rule: a leading `X` followed by an upper-case letter is a word of its own. Each name is compared with the header's name on the wire without regard to case, as HTTP compares them.

## Result

| Go field | wire | as built | | X-prefix rule | |
|---|---|---|---|---|---|
| `XAPIKey` | `X-API-Key` | `Xapi-Key` | **wrong** | `X-Api-Key` | ok |
| `XApiKey` | `X-Api-Key` | `X-Api-Key` | ok | `X-Api-Key` | ok |
| `XRequestID` | `X-Request-ID` | `X-Request-Id` | ok | `X-Request-Id` | ok |
| `XCSRFToken` | `X-CSRF-Token` | `Xcsrf-Token` | **wrong** | `X-Csrf-Token` | ok |
| `XXSRFToken` | `X-XSRF-TOKEN` | `Xxsrf-Token` | **wrong** | `X-Xsrf-Token` | ok |
| `XHTTPMethodOverride` | `X-HTTP-Method-Override` | `Xhttp-Method-Override` | **wrong** | `X-Http-Method-Override` | ok |
| `XForwardedFor` | `X-Forwarded-For` | `X-Forwarded-For` | ok | `X-Forwarded-For` | ok |
| `XRealIP` | `X-Real-IP` | `X-Real-Ip` | ok | `X-Real-Ip` | ok |
| `XAmzDate` | `X-Amz-Date` | `X-Amz-Date` | ok | `X-Amz-Date` | ok |
| `XHubSignature256` | `X-Hub-Signature-256` | `X-Hub-Signature256` | **wrong** | `X-Hub-Signature256` | **wrong** |
| `XGitHubEvent` | `X-GitHub-Event` | `X-Git-Hub-Event` | **wrong** | `X-Git-Hub-Event` | **wrong** |
| `XSignature` | `X-Signature` | `X-Signature` | ok | `X-Signature` | ok |
| `XMLVersion` | `XML-Version` | `Xml-Version` | ok | `X-Ml-Version` | **wrong** |
| `IfNoneMatch` | `If-None-Match` | `If-None-Match` | ok | `If-None-Match` | ok |
| `ETag` | `ETag` | `E-Tag` | **wrong** | `E-Tag` | **wrong** |
| `WWWAuthenticate` | `WWW-Authenticate` | `Www-Authenticate` | ok | `Www-Authenticate` | ok |
| `DNT` | `DNT` | `Dnt` | ok | `Dnt` | ok |
| `CFConnectingIP` | `CF-Connecting-IP` | `Cf-Connecting-Ip` | ok | `Cf-Connecting-Ip` | ok |
| `Authorization` | `Authorization` | `Authorization` | ok | `Authorization` | ok |

## What it shows

As built, every `X-` header whose next word is an initialism is read from the wrong name (4 of the 19). The X-prefix rule fixes those four and breaks `XMLVersion`, a name chosen to find the rule's edge; no common request header begins with an `X` that is part of an initialism. Three names stay wrong under either rule: a camel-cased brand (`GitHub`), a digit run that is a word of its own (`256`), and `ETag`. They need `WireNames`, which already applies to a Header section.

The list is chosen by hand from headers an API reads; it is evidence of the rule's edges, not a survey of how often each occurs.
