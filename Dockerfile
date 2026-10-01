# syntax=docker/dockerfile:1.7

# Build targets: `hub` and `agent`.
#   docker build --target hub   -t ghcr.io/idestis/eddy-hub .
#   docker build --target agent -t ghcr.io/idestis/eddy-agent .

# ---- SPA ----------------------------------------------------------------
# The Vite build writes to ../internal/ui/dist (relative to web/), which the hub embeds.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
# Node 25+ no longer ships corepack: install the pnpm version package.json pins.
RUN npm install -g --no-fund --no-audit "pnpm@$(node -p 'require("./package.json").packageManager.split("@")[1]')"
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY design/ /src/design/
COPY web/ ./
RUN pnpm run build

# ---- Go binaries ----------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=web /src/internal/ui/dist/ internal/ui/dist/
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w -X github.com/idestis/eddy/internal/version.Version=${VERSION}" \
      -o /out/eddy-hub ./cmd/hub && \
    go build -trimpath -ldflags "-s -w -X github.com/idestis/eddy/internal/version.Version=${VERSION}" \
      -o /out/eddy-agent ./cmd/agent

# ---- Runtime images -------------------------------------------------------
# distroless/static has no shell and no package manager. 65532 is the `nonroot` user.
FROM gcr.io/distroless/static-debian12:nonroot AS hub
COPY --from=build /out/eddy-hub /eddy-hub
USER 65532:65532
EXPOSE 8080 8443 9090
ENTRYPOINT ["/eddy-hub"]

FROM gcr.io/distroless/static-debian12:nonroot AS agent
COPY --from=build /out/eddy-agent /eddy-agent
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/eddy-agent"]
