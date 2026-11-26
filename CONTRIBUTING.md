# Contributing to clrd

Thanks for helping. Bug reports, ideas, docs fixes and code are all welcome.

## Before you start

- **Bugs and ideas:** open an issue with the matching template. Search first;
  it may already be filed.
- **Security problems:** do not open a public issue. See [SECURITY.md](SECURITY.md).
- **Bigger changes:** open an issue first so we can agree on the approach.
  [docs/SPEC.md](docs/SPEC.md) is the contract every part builds against (API,
  sync model, states, auth). A change to how parts talk to each other starts
  with a change to the spec.

## Build and test

| Area | Needs | Command |
|---|---|---|
| Server and web | Go 1.24+ | `go vet ./... && go test ./...` |
| Android | JDK 17, Android SDK 35 | `cd android && ./gradlew assembleDebug testDebugUnitTest lintDebug` |
| Go SDK | Go 1.23+ | `cd sdk/go && go test ./...` |
| JS SDK | Node 22 | `cd sdk/js && node --test test.js` |
| Python SDK | Python 3.12 | `cd sdk/python && python -m unittest test_clrd.py` |

Run a local server with `CGO_ENABLED=0 go build -o clrd ./server/cmd/clrd && ./clrd`.
[docs/self-host.md](docs/self-host.md) covers the settings.

## Pull requests

- One focused change per pull request, with a test that fails without it.
- Run `gofmt` on Go code. Match the style of the code around your change.
- **Keep the docs true.** If your change is something a user would notice, update
  in the same pull request:
  - One new file in [changelog.d](changelog.d/README.md). CI checks changes to
    `server/`, `web/`, `android/` and `sdk/`. For tests, CI and refactors
    with no user-visible change, use the `no-changelog` label.
  - `README.md` and the landing page (`web/templates/landing.html`) if a
    headline feature changes.
  - The matching page in `docs/`. The website serves `docs/` directly, so
    fixing the file fixes the site.
- Plain writing in docs and page copy: short sentences, no em dashes, en dashes
  or ellipsis characters (a test checks the web pages).

## License

By contributing you agree that your work is released under the
[Apache-2.0 license](LICENSE).
