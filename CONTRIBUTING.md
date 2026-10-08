# Contributing

Issues and pull requests are welcome.

## How this repository is maintained

For now this repository is a **mirror** of an upstream monorepo where the code is developed. In practice:

- Open issues and pull requests here as usual. They are reviewed here.
- An accepted pull request is applied to the upstream source by the maintainer and comes back in the next sync, so you will see
  your change land on `main` as part of a `sync:` commit. Your authorship is kept in the commit trailer
  (`Co-authored-by`) of the sync commit.
- Because of this, please keep pull requests small and focused, and expect merges to take a little longer than usual.

## Before you open a pull request

Run the checks CI runs (Go 1.27):

```sh
go build ./... && go vet ./...
go run ./cmd/sde -out data/sde/sde.sqlite      # one-time, builds the real SDE (about 440 MB)
EVE_REQUIRE_SDE=1 go test ./...
golangci-lint run ./...
go generate ./... && git diff --exit-code      # generated files (OpenAPI, llms.txt, tool schemas) must be committed
```

Tests that need the SDE skip when it is missing; CI sets `EVE_REQUIRE_SDE=1` so they fail instead. New behaviour comes with a
test, and a bug fix comes with the regression test in the same commit.

## Sign-off (DCO)

Please sign off your commits (`git commit -s`), which adds a `Signed-off-by:` line certifying the
[Developer Certificate of Origin](https://developercertificate.org/): you wrote the change or have the right to submit it
under the MIT licence of this repository.

## Data and licensing

- Do not add game data dumps, SDE databases, scraped community fits, or anything under a licence incompatible with MIT.
  See `NOTICE` for how CCP / EVE material is handled.
- Do not add any dependency that is not under a permissive licence (MIT, BSD, Apache-2.0, ISC, MPL-2.0); CI checks it.
- English only: code, comments, commit messages, and docs.
