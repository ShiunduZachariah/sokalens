package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alertevaluator"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/incidents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/notifications"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/sysmetrics"
)

func buildAlertEvaluator(cfg config.Config, db *sql.DB, logger *slog.Logger) (*alertevaluator.Evaluator, error) {
	if !cfg.TelegramEnabled || db == nil {
		return nil, nil
	}
	metricsClient, err := sysmetrics.New(cfg.PrometheusURL)
	if err != nil {
		return nil, fmt.Errorf("alert metrics client: %w", err)
	}
	incidentStore, err := incidents.NewPostgresRepository(incidents.SQLDB{DB: db}, cfg.IncidentCorrelationWindow)
	if err != nil {
		return nil, fmt.Errorf("alert incident store: %w", err)
	}
	stateMachine, err := incidents.NewPostgresStateMachine(incidents.SQLDB{DB: db})
	if err != nil {
		return nil, fmt.Errorf("alert recovery state machine: %w", err)
	}
	recoveryStore, err := incidents.NewPostgresRecoveryStore(incidents.SQLQueryer{DB: db}, stateMachine)
	if err != nil {
		return nil, fmt.Errorf("alert recovery store: %w", err)
	}
	notificationStore, err := notifications.NewPostgresStore(notifications.SQLDB{DB: db})
	if err != nil {
		return nil, fmt.Errorf("alert notification store: %w", err)
	}
	return alertevaluator.New(metricsClient, incidentStore, notificationStore, recoveryStore, alertevaluator.DefaultRules(), logger, time.Now)
}
