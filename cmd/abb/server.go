package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/brojonat/affiliate-bounty-board/http"
	"github.com/urfave/cli/v2"
	"go.temporal.io/sdk/client"
	sdklog "go.temporal.io/sdk/log"
)

const (
	EnvServerPort        = "SERVER_PORT"
	EnvTemporalAddress   = "TEMPORAL_ADDRESS"
	EnvTemporalNamespace = "TEMPORAL_NAMESPACE"
	EnvCORSHaders        = "CORS_HEADERS"
	EnvCORSMethods       = "CORS_METHODS"
	EnvCORSOrigins       = "CORS_ORIGINS"
	EnvLogLevel          = "LOG_LEVEL"
)

// parseLogLevel converts a string log level to slog.Level
// Defaults to slog.LevelWarn for unrecognized values.
func parseLogLevel(levelStr string) slog.Level {
	switch strings.ToUpper(levelStr) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		// Log a message here if you want to indicate an invalid level was used and warn is the default
		// For now, just defaulting silently.
		return slog.LevelWarn
	}
}

func serverCommands() []*cli.Command {
	return []*cli.Command{
		{
			Name:  "http-server",
			Usage: "Run the HTTP server",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "port",
					Aliases: []string{"p"},
					Usage:   "Port to listen on",
					EnvVars: []string{EnvServerPort},
				},
				&cli.StringFlag{
					Name:    "temporal-address",
					Aliases: []string{"ta"},
					Usage:   "Temporal server address",
					EnvVars: []string{EnvTemporalAddress},
					Value:   "localhost:7233",
				},
				&cli.StringFlag{
					Name:    "temporal-namespace",
					Aliases: []string{"tn"},
					Usage:   "Temporal namespace",
					EnvVars: []string{EnvTemporalNamespace},
					Value:   "default",
				},
				&cli.StringFlag{
					Name:    "log-level",
					Aliases: []string{"ll"},
					Usage:   "Set the logging level (DEBUG, INFO, WARN, ERROR)",
					EnvVars: []string{EnvLogLevel},
					Value:   "WARN",
				},
			},
			Action: run_server,
		},
	}
}

func run_server(c *cli.Context) error {
	// Create a context that can be cancelled
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Parse log level from flag
	logLevelStr := c.String("log-level")
	parsedLogLevel := parseLogLevel(logLevelStr)

	// Set up main application logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsedLogLevel}))

	// Set up Temporal client specific logger
	temporalSlogHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parsedLogLevel,
	})
	temporalLogger := sdklog.NewStructuredLogger(slog.New(temporalSlogHandler))

	// Set up Temporal client with retries
	var tc client.Client
	var err error
	maxRetries := 5
	retryInterval := 5 * time.Second
	temporalAddress := c.String("temporal-address")
	temporalNamespace := c.String("temporal-namespace")

	for i := 0; i < maxRetries; i++ {
		tc, err = client.Dial(client.Options{
			Logger:    temporalLogger,
			HostPort:  temporalAddress,
			Namespace: temporalNamespace,
		})
		if err == nil {
			logger.Info("Successfully connected to Temporal", "address", temporalAddress, "namespace", temporalNamespace)
			break
		}
		logger.Error("Failed to connect to Temporal", "attempt", i+1, "max_attempts", maxRetries, "error", err)
		if i < maxRetries-1 {
			logger.Info("Retrying Temporal connection", "interval", retryInterval)
			time.Sleep(retryInterval)
		}
	}
	if err != nil {
		return fmt.Errorf("failed to create temporal client after %d attempts: %w", maxRetries, err)
	}
	defer tc.Close()

	// Parse CORS configuration from environment variables
	normalizeCORSParams := func(e string) []string {
		params := strings.Split(e, ",")
		for i, p := range params {
			params[i] = strings.TrimSpace(p)
		}
		return params
	}

	headers := normalizeCORSParams(os.Getenv(EnvCORSHaders))
	methods := normalizeCORSParams(os.Getenv(EnvCORSMethods))
	origins := normalizeCORSParams(os.Getenv(EnvCORSOrigins))

	// Add CORS config to context
	ctx = http.WithCORSConfig(ctx, headers, methods, origins)

	// Run the server
	port := c.String("port")
	if port == "" {
		port = "8080"
	}
	// Pass Solana config values to RunServer (signature still needs update in http.go)
	return http.RunServer(ctx, logger, tc, port)
}
