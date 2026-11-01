FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY services/api/go.mod ./
COPY services/api/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY services/api/ ./
# Persist the module and compile caches between builds. Without these every
# deploy relinks the whole dependency graph from source, which is most of the
# build time and none of the value.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/api ./cmd/api

FROM alpine:3.23
RUN adduser -D -H appuser
USER appuser
COPY --from=build /out/api /usr/local/bin/api
COPY packages/contracts/openapi.yaml /contracts/openapi.yaml
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD wget --quiet --output-document=- http://127.0.0.1:8080/readyz | grep -q ready
ENTRYPOINT ["api"]
