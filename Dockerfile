# syntax=docker/dockerfile:1

# ── Admin dashboard build stage ────────────────────────────
FROM node:22-alpine AS ui

WORKDIR /ui

COPY admin-ui/package.json admin-ui/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci

COPY admin-ui/ .
RUN npm run build

# ── Build stage ────────────────────────────────────────────
FROM golang:1.25-alpine AS build

WORKDIR /src

# Cache dependencies separately from source for faster rebuilds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# The built dashboard is embedded into the binary via go:embed.
COPY --from=ui /ui/dist admin-ui/dist

# Pure-Go build (modernc.org/sqlite needs no cgo), so we can produce a fully
# static binary and ship it on a minimal base. Trim symbols to shrink it.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/macroflow-sync .

# Pre-create the data dir owned by the runtime user (65532 = distroless
# nonroot). Copying it into the final image gives named Docker volumes the
# right ownership out of the box.
RUN mkdir -p /data && chown 65532:65532 /data

# ── Runtime stage ──────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

# Link the published GHCR image back to its source repo. GitHub uses this label
# to associate the package with the repository (and inherit its visibility).
LABEL org.opencontainers.image.source=https://github.com/herrderkekse/MacroFlow-Backend

# Persist the SQLite database here; mount a volume at /data.
ENV DB_PATH=/data/macroflow.db \
    PORT=8080

COPY --from=build /out/macroflow-sync /macroflow-sync
COPY --from=build --chown=65532:65532 /data /data
WORKDIR /data

EXPOSE 8080
USER nonroot:nonroot
VOLUME ["/data"]

ENTRYPOINT ["/macroflow-sync"]
