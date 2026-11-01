FROM golang:1.27.3-alpine AS build
ARG OPS_BUILD_VERSION=dev
WORKDIR /src
COPY services/ops-controller/go.mod ./
COPY services/ops-controller/ ./
# Persist the module and compile caches between builds. Without these every
# deploy relinks the whole dependency graph from source, which is most of the
# build time and none of the value.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildVersion=${OPS_BUILD_VERSION}" -o /out/ops-controller ./cmd/ops-controller

FROM alpine:3.23
# tzdata: the bridge renders operator-facing times in Africa/Nairobi.
# Without it time.LoadLocation fails and every timestamp silently falls
# back to UTC — a three-hour offset on an incident time, which is worst
# exactly when it matters most.
RUN apk add --no-cache tzdata
RUN adduser -D -H -s /sbin/nologin appuser
USER appuser
COPY --from=build /out/ops-controller /usr/local/bin/ops-controller
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD wget --quiet --output-document=- http://127.0.0.1:8090/readyz | grep -q ready
ENTRYPOINT ["ops-controller"]
