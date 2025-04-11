# oke-gateway-api

[![Build](https://github.com/gemyago/oke-gateway-api/actions/workflows/build-flow.yml/badge.svg)](https://github.com/gemyago/oke-gateway-api/actions/workflows/build-flow.yml)
[![Coverage](https://raw.githubusercontent.com/gemyago/oke-gateway-api/test-artifacts/coverage/golang-coverage.svg)](https://htmlpreview.github.io/?https://raw.githubusercontent.com/gemyago/oke-gateway-api/test-artifacts/coverage/golang-coverage.html)

[Gateway API](https://gateway-api.sigs.k8s.io/) implementation for [Oracle Kubernetes (OKE)](https://www.oracle.com/cloud/cloud-native/kubernetes-engine/).

Project status: **Early Alpha**

## Contributing

### Project Setup

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