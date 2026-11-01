FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY services/worker/go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY services/worker/ ./
# Persist the module and compile caches between builds. Without these every
# deploy relinks the whole dependency graph from source, which is most of the
# build time and none of the value.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/worker ./cmd/worker

FROM alpine:3.23
RUN apk add --no-cache python3 py3-pip \
  && python3 -m venv /opt/prediction-engine-venv \
  && adduser -D -H appuser
COPY --chown=appuser:appuser services/prediction-engine /srv/prediction-engine
RUN /opt/prediction-engine-venv/bin/pip install --no-cache-dir -r /srv/prediction-engine/requirements.txt \
  && PYTHONPATH=/srv/prediction-engine /opt/prediction-engine-venv/bin/python -c "import prediction_engine.cli"
ENV PATH="/opt/prediction-engine-venv/bin:${PATH}" \
  PYTHONPATH="/srv/prediction-engine" \
  PREDICTION_ENGINE_CLI_COMMAND="python" \
  PREDICTION_ENGINE_CLI_WORKING_DIR="/srv/prediction-engine" \
  PREDICTION_ENGINE_CLI_PYTHONPATH="/srv/prediction-engine"
COPY --from=build /out/worker /usr/local/bin/worker
USER appuser
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["/usr/local/bin/worker", "healthcheck"]
ENTRYPOINT ["worker"]
