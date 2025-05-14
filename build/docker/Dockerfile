# It is assumed that the build context is the root of the project

ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:latest
FROM --platform=$BUILDPLATFORM $RUNTIME_IMAGE
ARG TARGETOS
ARG TARGETARCH
ARG TARGET_BINARY=server

COPY build/dist/${TARGETOS}/${TARGETARCH}/${TARGET_BINARY} /usr/local/bin/entrypoint
ENTRYPOINT ["entrypoint"]