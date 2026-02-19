package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/mirceanton/homer-sync/internal/config"
	"github.com/mirceanton/homer-sync/internal/controller"
	"github.com/mirceanton/homer-sync/internal/k8s"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "homer-sync",
		Short: "Automatically generate a Homer dashboard config from Kubernetes HTTPRoutes",
		RunE:  runE,
	}

	// Flags – names mirror the env-var suffix (HOMER_SYNC_<FLAG>).
	f := cmd.Flags()
	f.StringSlice("gateway-names", nil,
		"Comma-separated gateway names to filter HTTPRoutes by (opt-out mode when set)")
	f.StringSlice("domain-suffixes", nil,
		"Comma-separated domain suffixes to filter hostnames by (e.g. .home.example.com)")
	f.String("configmap-name", "homer-config",
		"Name of the ConfigMap to write the Homer config into")
	f.String("configmap-namespace", "",
		"Namespace for the ConfigMap (auto-detected from service account when empty)")
	f.Bool("daemon", true,
		"Run continuously; set to false to exit after one sync")
	f.Int("scan-interval", 300,
		"Seconds between scans in daemon mode")
	f.String("log-level", "info",
		"Log verbosity: debug, info, warn, error")
	f.String("title", "Home Dashboard",
		"Homer dashboard title")
	f.String("subtitle", "",
		"Homer dashboard subtitle")
	f.Int("columns", 5,
		"Number of service columns in the Homer layout")
	f.String("template-path", "",
		"Path to a custom Go template file; falls back to the built-in template when empty")

	// Bind each flag to its canonical env var, preserving backward compatibility
	// with the Python-era variable names.
	bindEnv := func(flag, env string) {
		if err := viper.BindPFlag(flag, f.Lookup(flag)); err != nil {
			panic(fmt.Sprintf("bind flag %q: %v", flag, err))
		}
		if err := viper.BindEnv(flag, env); err != nil {
			panic(fmt.Sprintf("bind env %q: %v", env, err))
		}
	}

	bindEnv("gateway-names", "HOMER_SYNC_GATEWAY_NAMES")
	bindEnv("domain-suffixes", "HOMER_SYNC_DOMAIN_SUFFIXES")
	bindEnv("configmap-name", "HOMER_SYNC_CONFIGMAP_NAME")
	bindEnv("configmap-namespace", "HOMER_SYNC_CONFIGMAP_NAMESPACE")
	bindEnv("daemon", "HOMER_SYNC_DAEMON_MODE")
	bindEnv("scan-interval", "HOMER_SYNC_SCAN_INTERVAL")
	bindEnv("log-level", "HOMER_SYNC_LOG_LEVEL")
	bindEnv("title", "HOMER_SYNC_TITLE")
	bindEnv("subtitle", "HOMER_SYNC_SUBTITLE")
	bindEnv("columns", "HOMER_SYNC_COLUMNS")
	bindEnv("template-path", "HOMER_SYNC_TEMPLATE_PATH")

	return cmd
}

func runE(cmd *cobra.Command, _ []string) error {
	cfg, err := buildConfig()
	if err != nil {
		return err
	}

	setupLogging(cfg.LogLevel)

	slog.Info("homer-sync starting",
		"daemon", cfg.Daemon,
		"interval", cfg.ScanInterval,
		"gateways", cfg.GatewayNames,
		"domain_suffixes", cfg.DomainSuffixes,
	)

	clients, err := k8s.NewClients()
	if err != nil {
		return fmt.Errorf("initialise kubernetes clients: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	ctrl := controller.New(clients, cfg)
	return ctrl.Run(ctx)
}

// buildConfig assembles Config from viper (flags + env vars).
func buildConfig() (*config.Config, error) {
	// viper returns comma-separated env vars as a single string; split manually.
	gatewayNames := splitList(viper.GetString("gateway-names"))
	if sl := viper.GetStringSlice("gateway-names"); len(sl) > 1 || (len(sl) == 1 && !strings.Contains(sl[0], ",")) {
		gatewayNames = filterEmpty(sl)
	}

	domainSuffixes := splitList(viper.GetString("domain-suffixes"))
	if sl := viper.GetStringSlice("domain-suffixes"); len(sl) > 1 || (len(sl) == 1 && !strings.Contains(sl[0], ",")) {
		domainSuffixes = filterEmpty(sl)
	}

	ns := viper.GetString("configmap-namespace")
	if ns == "" {
		ns = config.DetectNamespace()
	}

	return &config.Config{
		GatewayNames:       gatewayNames,
		DomainSuffixes:     domainSuffixes,
		ConfigMapName:      viper.GetString("configmap-name"),
		ConfigMapNamespace: ns,
		Daemon:             viper.GetBool("daemon"),
		ScanInterval:       viper.GetInt("scan-interval"),
		LogLevel:           config.ParseLogLevel(viper.GetString("log-level")),
		Title:              viper.GetString("title"),
		Subtitle:           viper.GetString("subtitle"),
		Columns:            viper.GetInt("columns"),
		TemplatePath:       viper.GetString("template-path"),
	}, nil
}

func setupLogging(level slog.Level) {
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
}

// splitList splits a comma-separated string into a trimmed, non-empty slice.
func splitList(s string) []string {
	return filterEmpty(strings.Split(s, ","))
}

func filterEmpty(ss []string) []string {
	out := ss[:0]
	for _, s := range ss {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
