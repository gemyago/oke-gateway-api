# It is assumed that the build context is the root of the project

# To change the runtime image, modify build.cfg (docker_runtime_image)
ARG RUNTIME_IMAGE=scratch

FROM --platform=$BUILDPLATFORM $RUNTIME_IMAGE
ARG TARGETOS
ARG TARGETARCH
ARG TARGET_BINARY=server

COPY build/dist/${TARGETOS}/${TARGETARCH}/${TARGET_BINARY} /usr/local/bin/entrypoint
ENTRYPOINT ["entrypoint"]