# Security

## Reporting a vulnerability

Report a vulnerability privately, through GitHub's private vulnerability reporting: on the repository's **Security** tab, choose **Report a vulnerability** ([github.com/nevindra/bites/security](https://github.com/nevindra/bites/security)). Do not open a public issue or pull request for it.

Say what is affected (the package or module, and the commit), what a client or an application has to do to trigger it, and what happens. A request that reproduces it, or a test in the style of the repository's own (`bitetest`, or the in-memory connections of [ADR 0027](docs/adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)), is the most useful form.

## Supported versions

bites is pre-1.0 and has no tag yet ([ADR 0034](docs/adr/0034-versioning-before-1-0.md)). Until the first tag, only the head of the default branch is supported: a fix lands there and nowhere else. Once tags exist, this section names the versions that receive fixes.

## Scope

In scope is the code in this repository that an application runs:

- the HTTP/1.1 engine and its parser, TLS and the PROXY protocol;
- the JSON codec;
- WebSocket;
- multipart, uploads and the other ways a request body is read;
- the security middleware and Providers: CORS, CSRF, rate limiting, security headers, sessions, the auth Providers and trusted proxies;
- the modules under `modules/`.

Out of scope: the programs under `bench/`, `examples/` and `docs/research/`, which measure or show the framework and are not meant to be deployed as they are; and a problem only in a dependency of a module, which belongs to that dependency's project (tell us too if bites makes it reachable).

## What to expect

The report is acknowledged on the advisory GitHub opens for it, and discussed there in private. A confirmed vulnerability is fixed on the default branch with a test that reproduces it, so it cannot come back unnoticed ([ADR 0033](docs/adr/0033-the-rules-are-build-steps.md)). The advisory is then published, crediting the reporter unless they ask not to be named. If the report is not a vulnerability, the advisory says why before it is closed.
