# syntax=docker/dockerfile:1

# ---- Build stage ----
# Compiles a static binary.
#
# NOTE: we intentionally do NOT run `go mod tidy` here. `tidy` resolves the FULL
# module graph including the test-only dependencies of our dependencies, which we
# never compile — a single flaky proxy connection then fails the whole build.
# `go build -mod=mod` fetches only what is needed to COMPILE, wrapped in a retry
# loop to tolerate transient proxy "connection refused" errors.
FROM golang:1.23-alpine AS build
WORKDIR /src

ENV CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local

# Copy go.mod (and go.sum if present) first to leverage Docker layer caching.
COPY go.mod ./
COPY go.su[m] ./

COPY . .

RUN for i in 1 2 3 4 5; do \
        echo "build attempt $i..."; \
        go build -mod=mod -trimpath -ldflags="-s -w" -o /out/cart . && exit 0; \
        echo "attempt $i failed (likely transient proxy error); retrying in 5s..."; \
        sleep 5; \
    done; \
    echo "build failed after 5 attempts" && exit 1

# ---- Runtime stage ----
# distroless = no shell, no package manager, minimal attack surface.
# `nonroot` runs the process as a non-root user (good k8s security practice).
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/cart /app/cart

EXPOSE 8085
USER nonroot:nonroot
ENTRYPOINT ["/app/cart"]
