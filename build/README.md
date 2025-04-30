# Build

This folder contains the build tools for the project.

## Build Binaries

Golang binaries are build for platforms defined in [build.cfg](build.cfg) file (see `platforms` section).

## Docker

To enable multi-platform builds please enable [container image storage](https://docs.docker.com/build/building/multi-platform/#prerequisites) for your docker daemon.

Docker related configuration options are defined in [build.cfg](build.cfg) file (see options with `docker_` prefix).

### Building Docker Images

Depending on the registry, you may need to authenticate with the registry prior to pushing images. In case of github (ghcr.io), you can use the following command:

```sh
# Assuming you have GITHUB_TOKEN set in the env. Make sure to set the username.
echo $GITHUB_TOKEN | docker login ghcr.io -u <username> --password-stdin

# If you have gh cli configured, you can use:
gh auth token | docker login ghcr.io -u $(gh auth status | grep -o "account [^ ]*" | cut -d ' ' -f 2) --password-stdin
```

Use commands below to build docker images locally:
```sh
# Build artifacts first
make dist

# Build local images (for local testing)
make docker/.local-images

# Build and push images to a remote registry
make docker/.remote-images
```

## Build Scripts

The build scripts are located in the [scripts](scripts) folder.

If iterating on scripts, please make sure to run the tests:

```sh
# Run tests for all scripts
make test

# Run specific python tests
python -m unittest discover -v -s ./scripts/tests -k TestListVersions

# Run self-test for bash scripts
scripts/resolve-docker-tags.sh --self-test
```