package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"autoteam/internal/config"
	"autoteam/internal/generator"
	"autoteam/internal/logger"

	"github.com/joho/godotenv"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"
)

// Build-time variables (set by ldflags)
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

// Context key for storing config
type contextKey string

const configContextKey contextKey = "config"

func main() {
	// Load .env file if it exists (ignore errors for optional file)
	_ = godotenv.Load()

	app := &cli.Command{
		Name:    "autoteam",
		Usage:   "Universal AI Agent Management System",
		Version: fmt.Sprintf("%s (built %s, commit %s)", Version, BuildTime, GitCommit),
		Before:  setupContextWithLogger,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "log-level",
				Aliases: []string{"l"},
				Usage:   "Set log level (debug, info, warn, error)",
				Value:   "warn",
			},
		},
		Commands: []*cli.Command{
			{
				Name:   "generate",
				Usage:  "Generate compose.yaml from autoteam.yaml",
				Action: generateCommand,
			},
			{
				Name:   "up",
				Usage:  "Generate and start containers",
				Action: upCommand,
			},
			{
				Name:   "down",
				Usage:  "Stop containers",
				Action: downCommand,
			},
			{
				Name:   "init",
				Usage:  "Create sample autoteam.yaml",
				Action: initCommand,
			},
			{
				Name:   "agents",
				Usage:  "List all agents and their states",
				Action: agentsCommand,
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		// Create emergency logger for fatal errors
		if emergencyLogger, logErr := logger.NewLogger(logger.ErrorLevel); logErr == nil {
			emergencyLogger.Fatal("Application failed to run", zap.Error(err))
		} else {
			os.Exit(1)
		}
	}
}

func generateCommand(ctx context.Context, cmd *cli.Command) error {
	log := logger.FromContext(ctx)
	cfg := getConfigFromContext(ctx)
	if cfg == nil {
		log.Error("Config not available in context")
		return fmt.Errorf("config not available in context")
	}

	log.Info("Generating compose.yaml", zap.String("team_name", cfg.Settings.TeamName))
	gen := generator.New()
	if err := gen.GenerateCompose(cfg); err != nil {
		log.Error("Failed to generate compose.yaml", zap.Error(err))
		return fmt.Errorf("failed to generate compose.yaml: %w", err)
	}

	log.Info("Generated compose.yaml successfully")
	fmt.Println("Generated compose.yaml successfully")
	return nil
}

func upCommand(ctx context.Context, cmd *cli.Command) error {
	if err := generateCommand(ctx, cmd); err != nil {
		return err
	}

	fmt.Println("Starting containers...")
	if err := runDockerCompose(ctx, "up", "-d", "--remove-orphans"); err != nil {
		return fmt.Errorf("failed to start containers: %w", err)
	}

	fmt.Println("Containers started successfully")
	return nil
}

func downCommand(ctx context.Context, cmd *cli.Command) error {
	fmt.Println("Stopping containers...")
	if err := runDockerCompose(ctx, "down"); err != nil {
		return fmt.Errorf("failed to stop containers: %w", err)
	}

	fmt.Println("Containers stopped successfully")
	return nil
}

func initCommand(ctx context.Context, cmd *cli.Command) error {
	if err := config.CreateSampleConfig("autoteam.yaml"); err != nil {
		return fmt.Errorf("failed to create sample config: %w", err)
	}

	fmt.Println("Created sample autoteam.yaml")
	return nil
}

func agentsCommand(ctx context.Context, cmd *cli.Command) error {
	log := logger.FromContext(ctx)
	cfg := getConfigFromContext(ctx)
	if cfg == nil {
		log.Error("Config not available in context")
		return fmt.Errorf("config not available in context")
	}

	fmt.Println("Agents configuration:")
	fmt.Println()

	for i, agent := range cfg.Agents {
		status := "enabled"
		if !agent.IsEnabled() {
			status = "disabled"
		}

		fmt.Printf("%d. %s (%s)\n", i+1, agent.Name, status)
		fmt.Printf("   GitHub User: %s\n", agent.GitHubUser)
		if agent.Prompt != "" {
			// Show first line of prompt
			lines := strings.Split(agent.Prompt, "\n")
			if len(lines) > 0 && lines[0] != "" {
				prompt := lines[0]
				if len(prompt) > 80 {
					prompt = prompt[:77] + "..."
				}
				fmt.Printf("   Prompt: %s\n", prompt)
			}
		}
		fmt.Println()
	}

	// Summary
	enabledCount := 0
	for _, agent := range cfg.Agents {
		if agent.IsEnabled() {
			enabledCount++
		}
	}
	fmt.Printf("Total agents: %d (enabled: %d, disabled: %d)\n",
		len(cfg.Agents), enabledCount, len(cfg.Agents)-enabledCount)

	return nil
}

func runDockerCompose(ctx context.Context, args ...string) error {
	cfg := getConfigFromContext(ctx)

	// Use the compose.yaml file from .autoteam directory
	composeArgs := []string{"-f", config.ComposeFilePath}

	// If config is available, use custom project name, otherwise use default
	if cfg != nil && cfg.Settings.TeamName != "" {
		composeArgs = append(composeArgs, "-p", cfg.Settings.TeamName)
	}

	composeArgs = append(composeArgs, args...)

	cmd := exec.Command("docker-compose", composeArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// setupContextWithLogger sets up logger and loads config into context
func setupContextWithLogger(ctx context.Context, cmd *cli.Command) (context.Context, error) {
	// Setup logger first
	logLevelStr := cmd.String("log-level")
	logLevel, err := logger.ParseLogLevel(logLevelStr)
	if err != nil {
		return ctx, fmt.Errorf("invalid log level: %w", err)
	}

	ctx, err = logger.SetupContext(ctx, logLevel)
	if err != nil {
		return ctx, fmt.Errorf("failed to setup logger: %w", err)
	}

	log := logger.FromContext(ctx)
	log.Info("Starting autoteam",
		zap.String("version", Version),
		zap.String("build_time", BuildTime),
		zap.String("git_commit", GitCommit),
		zap.String("log_level", string(logLevel)),
	)

	// Skip loading config for init command as it creates the config file
	// Check command line arguments since Before hook runs on root command
	if len(os.Args) > 1 && os.Args[1] == "init" {
		return ctx, nil
	}

	cfg, err := config.LoadConfig("autoteam.yaml")
	if err != nil {
		log.Error("Failed to load config", zap.Error(err))
		return ctx, fmt.Errorf("failed to load config: %w", err)
	}

	log.Debug("Config loaded successfully", zap.String("team_name", cfg.Settings.TeamName))
	return context.WithValue(ctx, configContextKey, cfg), nil
}

// getConfigFromContext retrieves the config from context
func getConfigFromContext(ctx context.Context) *config.Config {
	cfg, ok := ctx.Value(configContextKey).(*config.Config)
	if !ok {
		return nil
	}
	return cfg
}
