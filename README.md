# golang-backend-boilerplate

[![Build](https://github.com/gemyago/golang-backend-boilerplate/actions/workflows/build-flow.yml/badge.svg)](https://github.com/gemyago/golang-backend-boilerplate/actions/workflows/build-flow.yml)
[![Coverage](https://raw.githubusercontent.com/gemyago/golang-backend-boilerplate/test-artifacts/coverage/golang-coverage.svg)](https://htmlpreview.github.io/?https://raw.githubusercontent.com/gemyago/golang-backend-boilerplate/test-artifacts/coverage/golang-coverage.html)

Basic golang boilerplate for backend projects.

Key features:
* [cobra](github.com/spf13/cobra) - CLI interactions
* [viper](github.com/spf13/viper) - Configuration management
* [apigen](github.com/gemyago/apigen) - API layer generator
* uber [dig](go.uber.org/dig) is used as DI framework
  * for small projects it may make sense to setup dependencies manually
* `slog` is used for logs
* [slog-http](github.com/samber/slog-http) is used to produce access logs
* [testify](github.com/stretchr/testify) and [mockery](github.com/vektra/mockery) are used for tests
* [gow](github.com/mitranim/gow) is used to watch and restart tests or server

## Starting a new project

* Clone the repo with a new name

* Replace module name with desired one. Example:

  ```bash
  # Manually specify desired module name
  find . -name "*.go" -o -name "go.mod" | xargs sed -i 's|github.com/gemyago/golang-backend-boilerplate|<YOUR-MODULE-PATH>|g';

  # Get module name matching repo
  export module_name=$(git remote get-url origin | sed -E \
    -e 's|^git@([^:]+):|\1/|' \
    -e 's|^https?://||' \
    -e 's|\.git$||')
   find . -name "*.go" -o -name "go.mod" | xargs gsed -i "s|github.com/gemyago/golang-backend-boilerplate|${module_name}|g";
  ```
  Note: on osx you may have to install and use [gnu sed](https://formulae.brew.sh/formula/gnu-sed). In such case you may need to replace `sed` with `gsed` above.

## Project structure

* [cmd/server](./cmd/server) is a main entrypoint to start the server
* [cmd/jobs](./cmd/jobs) is a main entrypoint to start jobs
* [internal/api/http](./internal/api/http) - includes http routes related stuff
  * [internal/api/http/v1routes.yaml](./internal/api/http/v1routes.yaml) - OpenAPI spec for the api routes. HTTP layer is generated with [apigen](github.com/gemyago/apigen)
* `internal/app` - place to add application layer code (e.g business logic).
* `internal/services` - lower level components are supposed to be here (e.g database access layer e.t.c).

## Project Setup

Please have the following tools installed: 
* [direnv](https://github.com/direnv/direnv) 
* [gobrew](https://github.com/kevincobain2000/gobrew#install-or-update)

Install/Update dependencies: 
```sh
# Install
go mod download
go install tool

# Update:
go get -u ./... && go mod tidy
```

### Build dependencies

This step is required if you plan to work on the build tooling. In this case please make sure to install:
* [pyenv](https://github.com/pyenv/pyenv?tab=readme-ov-file#installation).

```sh
# Install required python version
pyenv install -s

# Setup python environment
python -m venv .venv

# Reload env
direnv reload

# Install python dependencies
pip install -r requirements.txt
```

If updating python dependencies, please lock them:
```sh
pip freeze > requirements.txt
```

## Development

### Lint and Tests

Run all lint and tests:
```bash
make lint
make test
```

Run specific tests:
```bash
# Run once
go test -v ./internal/api/http/v1controllers/ --run TestHealthCheck

# Run same test multiple times
# This is useful to catch flaky tests
go test -v -count=5 ./internal/api/http/v1controllers/ --run TestHealthCheck

# Run and watch. Useful when iterating on tests
gow test -v ./internal/api/http/v1controllers/ --run TestHealthCheck
```
### Run local API server:

```bash
# Regular mode
go run ./cmd/server/ start

# Watch mode (double ^C to stop)
gow run ./cmd/server/ start
```