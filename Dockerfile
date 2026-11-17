# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine3.24 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/cloudflare-ddns ./cmd/cloudflare-ddns

FROM --platform=$BUILDPLATFORM alpine:3.24.4 AS rootfs
RUN apk add --no-cache ca-certificates \
 && mkdir -m 1777 /scratch-tmp

FROM scratch

COPY --from=rootfs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=rootfs --chmod=1777 /scratch-tmp /tmp
COPY --from=build /out/cloudflare-ddns /usr/local/bin/cloudflare-ddns

USER 10001:10001

# Soft heap limit below the 16 MB container limit, so the GC works harder
# before the OOM killer steps in. Override with -e GOMEMLIMIT=... if needed.
ENV GOMEMLIMIT=12MiB

HEALTHCHECK --start-period=30s --start-interval=5s --interval=1m --timeout=5s --retries=3 \
    CMD ["/usr/local/bin/cloudflare-ddns", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/cloudflare-ddns"]
