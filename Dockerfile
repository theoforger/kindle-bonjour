# syntax=docker/dockerfile:1
# kindle-bonjour server image. See PLAN.md §6.
#
# The build stage runs on the build machine's own platform and cross-compiles
# with GOOS/GOARCH, so multi-arch builds need no emulation:
#   docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 .

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY web/ web/
ARG TARGETOS TARGETARCH TARGETVARIANT
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w" -o /out/kindle-bonjour . \
 && mkdir /out/data

# distroless/static: CA certificates (HTTPS to ECCC), /etc/passwd with the
# nonroot user (uid 65532), no shell. Time zones are built into the binary
# (time/tzdata), so TZ works.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/kindle-bonjour /kindle-bonjour
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV DATA_DIR=/data PORT=8080
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s --retries=3 \
    CMD ["/kindle-bonjour", "-healthcheck"]
ENTRYPOINT ["/kindle-bonjour"]
