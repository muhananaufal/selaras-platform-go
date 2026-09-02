# syntax=docker/dockerfile:1

# Satu Dockerfile untuk seluruh unit. Unit yang dibangun dipilih lewat
# build arg, sehingga tidak ada sembilan berkas yang harus dijaga tetap
# sinkron satu sama lain.
ARG UNIT

# ---------------------------------------------------------------- build
FROM golang:1.27.1-alpine AS build
WORKDIR /src

# Dependensi disalin lebih dulu dan sendirian: selama go.mod tidak
# berubah, layer ini tetap terpakai meski seluruh source berubah.
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG UNIT
ARG VERSION=dev
ARG REVISION=unknown

# CGO dimatikan supaya binernya benar-benar statis. Itu syarat mutlak
# untuk distroless static: tidak ada libc di sana untuk ditautkan.
# -s -w membuang tabel simbol dan DWARF; ukurannya turun banyak dan
# profil pprof tetap bekerja karena pclntab tidak ikut dibuang.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" \
      -o /out/app ./cmd/${UNIT}

# ---------------------------------------------------------------- runtime
# static-debian12 tidak punya shell, package manager, maupun libc.
# Yang dibawanya hanya CA certificates, tzdata, dan /etc/passwd - dan CA
# itulah alasan `scratch` tidak dipakai: tanpanya TLS ke Gemini gagal.
FROM gcr.io/distroless/static-debian12:nonroot

ARG UNIT
ARG VERSION=dev
ARG REVISION=unknown

LABEL org.opencontainers.image.title="selaras-${UNIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/muhananaufal/selaras-platform-go"

COPY --from=build /out/app /app

# Berjalan sebagai nonroot (uid 65532) yang sudah disediakan image dasar.
USER nonroot:nonroot
EXPOSE 8080

# Tidak ada HEALTHCHECK: image ini tidak punya shell maupun curl untuk
# menjalankannya. Kesehatan diperiksa Kubernetes lewat probe HTTP ke
# /healthz dan /readyz.
ENTRYPOINT ["/app"]
