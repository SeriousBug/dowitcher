# syntax=docker/dockerfile:1

# --- Stage 1: build the SPA ---
FROM --platform=$BUILDPLATFORM node:26-alpine AS web
WORKDIR /app/web
# Node 26 no longer bundles corepack; install the pinned pnpm directly.
RUN npm install -g pnpm@10.20.0
# Install deps first for layer caching.
COPY web/package.json web/pnpm-lock.yaml* web/pnpm-workspace.yaml* ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
# generated.ts is committed; build produces web/dist.
RUN pnpm build

# --- Stage 2: build the static Go binary embedding the SPA ---
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the committed dist placeholder with the freshly built SPA. Without the
# rm, the placeholder index.html survives alongside the real build and wins.
RUN rm -rf web/dist
COPY --from=web /app/web/dist ./web/dist
# Everything is pure Go, so cross-compile on the build host rather than emulating.
#
# -tags nodynamic is load-bearing, not a tuning knob. The AVIF and WebP decoders
# reach for a system libavif/libwebp through purego, which emits
# //go:cgo_import_dynamic and yields a dynamically linked binary even under
# CGO_ENABLED=0. distroless/static has no loader, so that binary dies at exec
# with a bare "no such file or directory" -- it builds and pushes fine and fails
# on first run. The tag drops the dlopen path and leaves the embedded WASM
# decoder, which is what this image would fall back to anyway: there is no
# system libavif in here to find.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -tags nodynamic -ldflags="-s -w" -o /dowitcher ./cmd/dowitcher
# Create the data and library dirs here so they can be owned by the nonroot
# runtime user; a mounted named volume inherits this ownership. distroless has no
# shell, so there is no chance to mkdir at runtime.
RUN mkdir -p /data /library

# --- Stage 3: tiny runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /dowitcher /dowitcher
COPY --from=build --chown=65532:65532 /data /data
COPY --from=build --chown=65532:65532 /library /library
VOLUME ["/data", "/library"]
EXPOSE 8080
ENV DOWITCHER_DB=/data/dowitcher.db DOWITCHER_ADDR=:8080 DOWITCHER_DATA=/data DOWITCHER_LIBRARY=/library
ENTRYPOINT ["/dowitcher"]
