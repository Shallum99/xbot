package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/xdevplatform/xurl/api"
	"github.com/xdevplatform/xurl/auth"
	"github.com/xdevplatform/xurl/config"
	"github.com/xdevplatform/xurl/store"

	"github.com/Shallum99/xbot/bot"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:     "xbot",
		Short:   "AI-powered bug fix bot triggered from X",
		Version: version,
		Long: `xbot monitors X for trigger tweets and automatically fixes bugs using coding agents.

How it works:
  1. A user reports a bug on X (normal tweet)
  2. You reply with "fix: <description>" to trigger the bot
  3. The bot runs a coding agent (Claude Code, Codex, Gemini) to fix the bug
  4. A PR is created automatically

Setup:
  xbot auth --client-id YOUR_ID --client-secret YOUR_SECRET
  xbot init --handle your_handle --repo /path/to/project
  xbot start`,
	}

	rootCmd.AddCommand(authCmd())
	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(startCmd())
	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(serviceCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// newClient creates an xurl API client using the xurl auth config.
func newClient() (api.Client, api.RequestOptions) {
	cfg := config.NewConfig()
	a := auth.NewAuth(cfg)

	client := api.NewApiClient(cfg, a)
	opts := api.RequestOptions{
		AuthType: "oauth2",
		Verbose:  false, // H2: Never verbose in bot mode
	}

	return client, opts
}

// ─── auth ────────────────────────────────────────────────────────

func authCmd() *cobra.Command {
	var (
		clientID     string
		clientSecret string
	)

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with X (Twitter) API",
		Long: `Set up X API authentication. Registers your app credentials and runs the OAuth2 flow.

Get your Client ID and Client Secret from https://developer.x.com

Examples:
  xbot auth --client-id YOUR_ID --client-secret YOUR_SECRET`,
		Run: func(cmd *cobra.Command, args []string) {
			// L5: Support env vars to avoid exposing secrets in process args
			if clientID == "" {
				clientID = os.Getenv("XBOT_CLIENT_ID")
			}
			if clientSecret == "" {
				clientSecret = os.Getenv("XBOT_CLIENT_SECRET")
			}

			if clientID == "" || clientSecret == "" {
				fmt.Fprintf(os.Stderr, "\033[31mError: --client-id and --client-secret are required\033[0m\n")
				fmt.Fprintf(os.Stderr, "\nGet your credentials at https://developer.x.com\n")
				fmt.Fprintf(os.Stderr, "Or set XBOT_CLIENT_ID and XBOT_CLIENT_SECRET environment variables.\n")
				os.Exit(1)
			}

			// Register app in xurl's token store (~/.xurl)
			ts := store.NewTokenStore()
			appName := "xbot"

			if existing := ts.GetApp(appName); existing != nil {
				if err := ts.UpdateApp(appName, clientID, clientSecret); err != nil {
					fmt.Fprintf(os.Stderr, "\033[31mError updating app: %v\033[0m\n", err)
					os.Exit(1)
				}
			} else {
				if err := ts.AddApp(appName, clientID, clientSecret); err != nil {
					fmt.Fprintf(os.Stderr, "\033[31mError registering app: %v\033[0m\n", err)
					os.Exit(1)
				}
			}

			if err := ts.SetDefaultApp(appName); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError setting default app: %v\033[0m\n", err)
				os.Exit(1)
			}

			fmt.Println("App credentials saved.")
			fmt.Println("Starting OAuth2 flow — your browser will open...")
			fmt.Println()

			// Run OAuth2 PKCE flow
			cfg := config.NewConfig()
			a := auth.NewAuth(cfg).WithAppName(appName)
			a.WithTokenStore(ts)

			_, err := a.OAuth2Flow("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mOAuth2 error: %v\033[0m\n", err)
				os.Exit(1)
			}

			fmt.Println()
			fmt.Printf("\033[32mAuthenticated!\033[0m\n")
			fmt.Printf("\nNext: run '\033[1mxbot init --handle your_handle --repo /path/to/project\033[0m'\n")
		},
	}

	cmd.Flags().StringVar(&clientID, "client-id", "", "X API Client ID (required)")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "X API Client Secret (required)")

	return cmd
}

// ─── init ────────────────────────────────────────────────────────

func initCmd() *cobra.Command {
	var (
		handle       string
		repo         string
		agent        string
		agentCmd     string
		pollInterval time.Duration
		branchPrefix string
		triggerKw    string
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize bot configuration",
		Long: `Set up the bot configuration. Creates ~/.xbot with your settings.

Only --handle is required. Everything else has sensible defaults:
  - Repo defaults to the current directory
  - Agent defaults to Claude Code
  - Trigger defaults to "fix:" (the keyword you reply with on X)

Examples:
  # Minimal — use defaults (current dir, Claude Code, "fix:" trigger)
  xbot init --handle shallum

  # Specify a different repo and agent
  xbot init --handle shallum --repo /path/to/project --agent codex

  # Use a custom trigger keyword
  xbot init --handle shallum --trigger "bug:"

  # Bring your own agent
  xbot init --handle shallum --agent custom --agent-cmd "my-tool --auto"`,
		Run: func(cmd *cobra.Command, args []string) {
			if handle == "" {
				fmt.Fprintf(os.Stderr, "\033[31mError: --handle is required\033[0m\n")
				os.Exit(1)
			}

			// Resolve relative repo path
			repoPath := repo
			if repoPath == "." || repoPath == "" {
				wd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
					os.Exit(1)
				}
				repoPath = wd
			} else {
				absPath, err := filepath.Abs(repoPath)
				if err == nil {
					repoPath = absPath
				}
			}

			cfg := &bot.BotConfig{
				Handle:         handle,
				TriggerKeyword: triggerKw,
				Repo:           repoPath,
				Agent:          agent,
				AgentCmd:       agentCmd,
				PollInterval:   pollInterval,
				BranchPrefix:   branchPrefix,
				DryRun:         dryRun,
			}

			if err := cfg.Validate(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mConfig error: %v\033[0m\n", err)
				os.Exit(1)
			}

			if err := cfg.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}

			fmt.Printf("\033[32mBot configured!\033[0m\n\n")
			fmt.Printf("  Handle:    @%s\n", cfg.Handle)
			fmt.Printf("  Trigger:   %s\n", cfg.TriggerKeyword)
			fmt.Printf("  Repo:      %s\n", cfg.Repo)
			fmt.Printf("  Agent:     %s\n", cfg.Agent)
			fmt.Printf("  Interval:  %s\n", cfg.PollInterval)

			fmt.Printf("\nRun '\033[1mxbot start\033[0m' to begin monitoring.\n")
		},
	}

	cmd.Flags().StringVar(&handle, "handle", "", "Your X handle, without the @ (required)")
	cmd.Flags().StringVar(&repo, "repo", ".", "Path to the git repo the agent will fix bugs in")
	cmd.Flags().StringVar(&agent, "agent", "claude", "Which coding agent to use: claude, codex, gemini, or custom")
	cmd.Flags().StringVar(&agentCmd, "agent-cmd", "", "Command to run when --agent=custom (e.g. \"my-tool --auto\")")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 60*time.Second, "How often to check X for new trigger tweets")
	cmd.Flags().StringVar(&branchPrefix, "branch-prefix", "bot/fix-", "Prefix for git branches created by the agent")
	cmd.Flags().StringVar(&triggerKw, "trigger", "fix:", "The keyword you'll reply with on X to trigger a fix")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Log what would happen without actually running the agent")

	return cmd
}

// ─── start ───────────────────────────────────────────────────────

func startCmd() *cobra.Command {
	var (
		dryRun       bool
		once         bool
		pollInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the bot polling loop",
		Long: `Start monitoring X for trigger tweets and processing bug reports.

Examples:
  xbot start
  xbot start --once
  xbot start --dry-run
  xbot start --poll-interval 30s`,
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := bot.LoadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}

			if cmd.Flags().Changed("dry-run") {
				cfg.DryRun = dryRun
			}
			if cmd.Flags().Changed("poll-interval") {
				cfg.PollInterval = pollInterval
			}

			if err := cfg.Validate(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mConfig error: %v\033[0m\n", err)
				os.Exit(1)
			}

			agent, err := bot.NewAgent(cfg.Agent, cfg.AgentCmd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}

			client, opts := newClient()
			state, err := bot.LoadState()
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}
			logger := log.New(os.Stdout, "", log.LstdFlags)

			handler := &bot.Handler{
				Config: cfg,
				State:  state,
				Client: client,
				Opts:   opts,
				Agent:  agent,
				Logger: logger,
			}

			poller := &bot.Poller{
				Config:  cfg,
				State:   state,
				Client:  client,
				Opts:    opts,
				Handler: handler,
				Logger:  logger,
			}

			if once {
				if err := poller.RunOnce(context.Background()); err != nil {
					fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
					os.Exit(1)
				}
				return
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			if err := poller.Run(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Log only, don't execute agent")
	cmd.Flags().BoolVar(&once, "once", false, "Poll once and exit")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 0, "Override poll interval")

	return cmd
}

// ─── run ─────────────────────────────────────────────────────────

func runCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "run TWEET_ID_OR_URL",
		Short: "Process a single tweet through the bot pipeline",
		Long: `Process a specific tweet. Useful for testing the full pipeline.

Examples:
  xbot run 1234567890
  xbot run https://x.com/user/status/1234567890
  xbot run 1234567890 --dry-run`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := bot.LoadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}

			if cmd.Flags().Changed("dry-run") {
				cfg.DryRun = dryRun
			}

			if err := cfg.Validate(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mConfig error: %v\033[0m\n", err)
				os.Exit(1)
			}

			agent, err := bot.NewAgent(cfg.Agent, cfg.AgentCmd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}

			client, opts := newClient()
			logger := log.New(os.Stdout, "", log.LstdFlags)

			// Fetch the tweet
			tweetID := bot.ResolvePostID(args[0])
			logger.Printf("[BOT] Fetching tweet %s...", tweetID)

			resp, err := bot.FetchTweet(client, tweetID, opts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError fetching tweet: %v\033[0m\n", err)
				os.Exit(1)
			}

			tweet, err := bot.ParseSingleTweet(resp)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError parsing tweet: %v\033[0m\n", err)
				os.Exit(1)
			}

			tweet.BugDescription = bot.ExtractBugDesc(tweet.Text, cfg.TriggerKeyword)

			state, err := bot.LoadState()
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}

			handler := &bot.Handler{
				Config: cfg,
				State:  state,
				Client: client,
				Opts:   opts,
				Agent:  agent,
				Logger: logger,
			}

			if err := handler.Process(context.Background(), *tweet); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Log only, don't execute agent")

	return cmd
}

// ─── status ──────────────────────────────────────────────────────

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show bot configuration and state",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := bot.LoadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}

			fmt.Println("\033[1mBot Configuration\033[0m")
			fmt.Printf("  Handle:         @%s\n", cfg.Handle)
			fmt.Printf("  Trigger:        %s\n", cfg.TriggerKeyword)
			fmt.Printf("  Repo:           %s\n", cfg.Repo)
			fmt.Printf("  Agent:          %s\n", cfg.Agent)
			if cfg.AgentCmd != "" {
				fmt.Printf("  Agent Cmd:      %s\n", cfg.AgentCmd)
			}
			fmt.Printf("  Poll Interval:  %s\n", cfg.PollInterval)
			fmt.Printf("  Branch Prefix:  %s\n", cfg.BranchPrefix)
			fmt.Printf("  Dry Run:        %v\n", cfg.DryRun)

			state, err := bot.LoadState()
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}
			fmt.Println("\n\033[1mBot State\033[0m")
			sinceID := state.SinceID
			if sinceID == "" {
				sinceID = "(none)"
			}
			fmt.Printf("  Since ID:       %s\n", sinceID)
			if !state.LastPollTime.IsZero() {
				fmt.Printf("  Last Poll:      %s\n", state.LastPollTime.Format(time.RFC3339))
			} else {
				fmt.Printf("  Last Poll:      (never)\n")
			}
			fmt.Printf("  Processed:      %d tweet(s)\n", len(state.ProcessedIDs))

			if len(state.ProcessedIDs) > 0 {
				fmt.Println("\n  Recent:")
				count := 0
				for id, status := range state.ProcessedIDs {
					if count >= 5 {
						fmt.Printf("  ... and %d more\n", len(state.ProcessedIDs)-5)
						break
					}
					fmt.Printf("    %s → %s\n", id, status)
					count++
				}
			}
		},
	}
}

// ─── service ─────────────────────────────────────────────────────

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage xbot as a background service (systemd/launchd)",
		Long: `Run xbot 24/7 as a background service that starts on boot.

Uses systemd on Linux and launchd on macOS — no sudo required.

Examples:
  xbot service install   # set up the background service
  xbot service start     # start it
  xbot service stop      # stop it
  xbot service status    # check if it's running
  xbot service logs      # tail the logs
  xbot service uninstall # remove the service`,
	}

	cmd.AddCommand(serviceInstallCmd())
	cmd.AddCommand(serviceUninstallCmd())
	cmd.AddCommand(serviceStartCmd())
	cmd.AddCommand(serviceStopCmd())
	cmd.AddCommand(serviceStatusCmd())
	cmd.AddCommand(serviceLogsCmd())

	return cmd
}

func serviceInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install xbot as a background service",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Verify bot config exists first
			if _, err := bot.LoadConfig(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				fmt.Fprintf(os.Stderr, "Run 'xbot init' first to configure the bot.\n")
				os.Exit(1)
			}

			if err := bot.ServiceInstall(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}

			paths, _ := bot.GetServicePaths()
			fmt.Printf("\033[32mService installed!\033[0m\n\n")
			fmt.Printf("  Config: %s\n", paths.ConfigPath)
			if paths.LogPath != "" {
				fmt.Printf("  Logs:   %s\n", paths.LogPath)
			}
			fmt.Printf("\nRun '\033[1mxbot service start\033[0m' to start the service.\n")
		},
	}
}

func serviceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the xbot background service",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := bot.ServiceUninstall(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}
			fmt.Printf("\033[32mService uninstalled.\033[0m\n")
		},
	}
}

func serviceStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the xbot background service",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := bot.ServiceStart(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}
			fmt.Printf("\033[32mService started.\033[0m\n")
			fmt.Printf("Run '\033[1mxbot service logs\033[0m' to watch output.\n")
		},
	}
}

func serviceStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the xbot background service",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := bot.ServiceStop(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}
			fmt.Printf("\033[32mService stopped.\033[0m\n")
		},
	}
}

func serviceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check if the xbot service is running",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := bot.ServiceStatus(); err != nil {
				os.Exit(1)
			}
		},
	}
}

func serviceLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Tail the xbot service logs",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := bot.ServiceLogs(); err != nil {
				fmt.Fprintf(os.Stderr, "\033[31mError: %v\033[0m\n", err)
				os.Exit(1)
			}
		},
	}
}
