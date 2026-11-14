# Contributing

Issues and pull requests are welcome.

## How this repository is maintained

This repository is the only source of the Go module `eve-cyno.dev/go/data`. Pull requests are reviewed and merged here, and
releases are tags of this repository (`vX.Y.Z`). The EVE-Cyno services consume the module by version like any other
dependency.

- Open issues and pull requests as usual. Keep them small and focused.

## Before you open a pull request

Run the checks CI runs (Go 1.27):

```sh
go build ./... && go vet ./...
go run ./cmd/sde -out data/sde/sde.sqlite      # one-time, builds the real SDE (about 440 MB)
EVE_REQUIRE_SDE=1 go test ./...
golangci-lint run ./...
go generate ./dataapi && git diff --exit-code  # generated files (OpenAPI, llms.txt, tool schemas, field docs) must be committed
```

After an SDE refresh (a new Fuzzwork build): regenerate the fix/downgrade golden. Solve the 96 pairs with the code at 1d01273 (a tree copy with
the corpus) on the new SDE, check that status, change count and To list of every pair match the current code, then run
`EVE_UPDATE_GOLDEN=1 go test -run TestFixDowngrade_UnchangedByTwoPass ./fit/solve/` and commit `fit/solve/testdata/fixdown-golden.json`. Until then the
test skips with a `skipped goldens: 1 (golden build X, SDE build Y)` line, which the CI step prints.

Tests that need the SDE skip when it is missing; CI sets `EVE_REQUIRE_SDE=1` so they fail instead. New behaviour comes with a
test, and a bug fix comes with the regression test in the same commit.

## Sign-off (DCO)

Please sign off your commits (`git commit -s`), which adds a `Signed-off-by:` line certifying the
[Developer Certificate of Origin](https://developercertificate.org/): you wrote the change or have the right to submit it
under the MIT licence of this repository.

## Data and licensing

- Do not add game data dumps, SDE databases, scraped community fits, or anything under a licence incompatible with MIT.
  See `NOTICE` for how EVE material is handled.
- Do not add any dependency that is not under a permissive licence (MIT, BSD, Apache-2.0, ISC, MPL-2.0); CI checks it.
- English only: code, comments, commit messages, and docs.
