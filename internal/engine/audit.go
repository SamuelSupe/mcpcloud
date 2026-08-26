package engine

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

type AuditEvent struct {
	CorrelationID string         `json:"correlation_id"`
	Provider      model.Provider `json:"provider"`
	Profile       string         `json:"profile"`
	Source        model.Source   `json:"source,omitempty"`
	Operation     string         `json:"operation"`
	Scope         string         `json:"scope,omitempty"`
	Region        string         `json:"region,omitempty"`
	Attempt       int            `json:"attempt"`
	Requests      int            `json:"requests"`
	DurationMS    int64          `json:"duration_ms"`
	Rows          int            `json:"rows"`
	Scanned       int            `json:"scanned"`
	Status        string         `json:"status"`
	ErrorCode     string         `json:"error_code,omitempty"`
	Retryable     bool           `json:"retryable,omitempty"`
}

type AuditSink interface {
	Record(context.Context, AuditEvent)
}

type AuditFunc func(context.Context, AuditEvent)

func (f AuditFunc) Record(ctx context.Context, event AuditEvent) { f(ctx, event) }

func NewSlogAuditSink(logger *slog.Logger) AuditSink {
	if logger == nil {
		return nil
	}
	return AuditFunc(func(_ context.Context, event AuditEvent) {
		logger.Info("cloud provider call",
			slog.String("correlation_id", event.CorrelationID),
			slog.String("provider", string(event.Provider)),
			slog.String("profile", event.Profile),
			slog.String("source", string(event.Source)),
			slog.String("operation", event.Operation),
			slog.String("scope", event.Scope),
			slog.String("region", event.Region),
			slog.Int("attempt", event.Attempt),
			slog.Int("requests", event.Requests),
			slog.Int64("duration_ms", event.DurationMS),
			slog.Int("rows", event.Rows),
			slog.Int("scanned", event.Scanned),
			slog.String("status", event.Status),
			slog.String("error_code", event.ErrorCode),
			slog.Bool("retryable", event.Retryable),
		)
	})
}

func auditError(err error) (code string, retryable bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", true
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", false
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		return safeAuditCode(providerErr.Code), providerErr.Retryable
	}
	return "provider_error", false
}

func safeAuditCode(value string) string {
	if value == "" {
		return "provider_error"
	}
	if len(value) > 128 {
		value = value[:128]
	}
	var result strings.Builder
	result.Grow(len(value))
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-', char == '.', char == ':':
			result.WriteRune(char)
		default:
			result.WriteByte('_')
		}
	}
	return result.String()
}

func (e *Engine) SetAuditSink(sink AuditSink) {
	e.audit = sink
}

func (e *Engine) recordAudit(ctx context.Context, event AuditEvent, page provider.Page, err error, started time.Time) {
	if e.audit == nil {
		return
	}
	event.DurationMS = time.Since(started).Milliseconds()
	event.Requests = max(1, page.Requests)
	event.Rows = len(page.Rows)
	event.Scanned = max(page.Scanned, len(page.Rows))
	event.Status = "ok"
	if err != nil {
		event.Status = "failed"
		event.ErrorCode, event.Retryable = auditError(err)
	}
	e.audit.Record(ctx, event)
}

func operationForSource(adapter provider.Adapter, source model.Source) string {
	for _, capability := range adapter.Capabilities() {
		if capability.Source == source && len(capability.Operations) > 0 {
			return capability.Operations[0]
		}
	}
	return string(source)
}

func sourceForOperation(adapter provider.Adapter, operation string) model.Source {
	for _, capability := range adapter.Capabilities() {
		for _, candidate := range capability.Operations {
			if candidate == operation {
				return capability.Source
			}
		}
	}
	return model.SourceResources
}

func scopeForNative(params map[string]any) string {
	for _, key := range []string{"scope", "account_id", "project_id", "subscription_id"} {
		if value, ok := params[key].(string); ok {
			return value
		}
	}
	if values, ok := params["subscriptions"].([]string); ok && len(values) == 1 {
		return values[0]
	}
	if values, ok := params["subscriptions"].([]any); ok && len(values) == 1 {
		value, _ := values[0].(string)
		return value
	}
	return ""
}
