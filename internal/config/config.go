package config

import (
	"log/slog"
	"os"
	"strings"
)

const (
	AnnotationPrefix = "home.mirceanton.com"
	saNamespaceFile  = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// Config holds all runtime configuration for homer-sync.
type Config struct {
	GatewayNames       []string
	DomainSuffixes     []string
	ConfigMapName      string
	ConfigMapNamespace string
	Daemon             bool
	ScanInterval       int
	LogLevel           slog.Level
	Title              string
	Subtitle           string
	Columns            int
	TemplatePath       string
}

// HasFilters returns true when at least one opt-out filter is active.
func (c *Config) HasFilters() bool {
	return len(c.GatewayNames) > 0 || len(c.DomainSuffixes) > 0
}

// ParseLogLevel converts a level string (debug/info/warn/error) to slog.Level.
// Unrecognised strings default to Info.
func ParseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// DetectNamespace reads the pod's own namespace from the service-account
// volume mount, falling back to "default".
func DetectNamespace() string {
	data, err := os.ReadFile(saNamespaceFile)
	if err != nil {
		return "default"
	}
	return strings.TrimSpace(string(data))
}
