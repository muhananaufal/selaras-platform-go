# syntax=docker/dockerfile:1

# One Dockerfile for every unit. The unit built is chosen through a build
# arg, so there are not nine files that have to be kept in sync with each
# other.
ARG UNIT

# ---------------------------------------------------------------- build
FROM golang:1.27.1-alpine AS build
WORKDIR /src

# The dependencies are copied first and on their own: as long as go.mod does
# not change, this layer stays cached even when all of the source changes.
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG UNIT
ARG VERSION=dev
ARG REVISION=unknown

# CGO is switched off so the binary is truly static. That is an absolute
# requirement for distroless static: there is no libc there to link against.
# -s -w drops the symbol table and DWARF; the size drops a lot and pprof
# profiles still work because pclntab is not dropped.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" \
      -o /out/app ./cmd/${UNIT}

# ---------------------------------------------------------------- runtime
# static-debian12 has no shell, package manager, or libc. All it carries is
# the CA certificates, tzdata, and /etc/passwd - and those CAs are the
# reason `scratch` is not used: without them TLS to Gemini fails.
FROM gcr.io/distroless/static-debian12:nonroot

ARG UNIT
ARG VERSION=dev
ARG REVISION=unknown

LABEL org.opencontainers.image.title="selaras-${UNIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/muhananaufal/selaras-platform-go"

COPY --from=build /out/app /app

# The migration files come along in EVERY image (a few dozen KB), so the
# `migrate` image can be built from the same Dockerfile and run as a Job in
# the cluster (deploy/k8s/jobs/migrate.yaml). cmd/migrate reads
# file://migrations/<service> relative to WORKDIR.
COPY migrations /migrations
WORKDIR /

# Runs as the nonroot user (uid 65532) the base image already provides.
USER nonroot:nonroot
EXPOSE 8080

# No HEALTHCHECK: this image has no shell or curl to run one. Health is
# checked by Kubernetes through HTTP probes to /healthz and /readyz.
ENTRYPOINT ["/app"]
