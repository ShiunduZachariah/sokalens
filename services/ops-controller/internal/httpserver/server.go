package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telemetry"
)

type Server struct {
	http            *http.Server
	logger          *slog.Logger
	metrics         *telemetry.Metrics
	shutdownTimeout time.Duration
	serving         atomic.Bool
	alertValidator  *alerts.Validator
	alertSink       alerts.Sink
	alertMaxBody    int64
	incidentTimeout time.Duration
}

func New(cfg config.Config, logger *slog.Logger, metrics *telemetry.Metrics) *Server {
	return NewWithAlertSink(cfg, logger, metrics, alerts.NoopSink{})
}

func NewWithAlertSink(cfg config.Config, logger *slog.Logger, metrics *telemetry.Metrics, sink alerts.Sink) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = telemetry.NewMetrics()
	}
	if sink == nil {
		sink = alerts.NoopSink{}
	}
	s := &Server{logger: logger, metrics: metrics, shutdownTimeout: cfg.ShutdownTimeout, alertValidator: alerts.NewValidator(cfg.AlertSigningSecret, cfg.AlertMaxClockSkew, cfg.AlertReplayLimit), alertSink: sink, alertMaxBody: cfg.AlertMaxBodyBytes, incidentTimeout: cfg.IncidentOperationTimeout}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /metrics", s.prometheus)
	mux.HandleFunc("POST /v1/alerts", s.ingestAlert)
	s.http = &http.Server{
		Addr:              cfg.Address,
		Handler:           s.observe(mux),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
	return s
}

func (s *Server) ingestAlert(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.rejectAlert(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.alertMaxBody))
	if err != nil {
		s.rejectAlert(w, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	deliveryID := r.Header.Get("X-Ops-Delivery-ID")
	accepted, err := s.alertValidator.Validate(r.Header.Get("X-Ops-Timestamp"), deliveryID, r.Header.Get("X-Ops-Signature"), body)
	if err != nil {
		status, reason := http.StatusBadRequest, "invalid"
		switch {
		case errors.Is(err, alerts.ErrDisabled):
			status, reason = http.StatusServiceUnavailable, "disabled"
		case errors.Is(err, alerts.ErrSignature):
			status, reason = http.StatusUnauthorized, "signature"
		case errors.Is(err, alerts.ErrStale):
			status, reason = http.StatusUnauthorized, "stale"
		case errors.Is(err, alerts.ErrReplay):
			status, reason = http.StatusConflict, "replay"
		case errors.Is(err, alerts.ErrCapacity):
			status, reason = http.StatusServiceUnavailable, "replay_capacity"
		}
		s.rejectAlert(w, status, reason)
		return
	}
	sinkCtx, cancel := context.WithTimeout(r.Context(), s.incidentTimeout)
	err = s.alertSink.Accept(sinkCtx, accepted)
	cancel()
	if err != nil {
		s.alertValidator.Release(deliveryID)
		s.rejectAlert(w, http.StatusServiceUnavailable, "sink_unavailable")
		return
	}
	s.metrics.ObserveAlert("accepted")
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "idempotency_key": accepted.IdempotencyKey})
}

func (s *Server) rejectAlert(w http.ResponseWriter, status int, reason string) {
	s.metrics.ObserveAlert(reason)
	writeJSON(w, status, map[string]any{"status": "rejected"})
}

func (s *Server) Handler() http.Handler { return s.http.Handler }
func (s *Server) Ready() bool           { return s.serving.Load() && s.metrics.Ready() }

// Serve runs until the listener fails or the context is cancelled. Cancellation
// first marks readiness false, then drains in-flight requests within the bound.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.serving.Store(true)
	s.metrics.SetReady(true)
	s.logger.Info("ops controller HTTP server ready", "address", listener.Addr().String())

	serveResult := make(chan error, 1)
	go func() { serveResult <- s.http.Serve(listener) }()

	select {
	case err := <-serveResult:
		s.serving.Store(false)
		s.metrics.SetReady(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		s.serving.Store(false)
		s.metrics.SetReady(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-serveResult
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "healthy"})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	if !s.Ready() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "autonomy_mode": config.ModeObserve})
}

func (s *Server) prometheus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.metrics.WritePrometheus(w)
}

func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		route := r.URL.Path
		switch route {
		case "/healthz", "/readyz", "/metrics", "/v1/alerts":
		default:
			route = "not_found"
		}
		s.metrics.ObserveHTTP(r.Method, route, wrapped.status)
		s.logger.Info("request completed", "method", r.Method, "route", route, "status", wrapped.status)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func Listen(address string) (net.Listener, error) {
	return net.Listen("tcp", address)
}

func Port(listener net.Listener) int {
	_, raw, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(raw)
	return port
}
