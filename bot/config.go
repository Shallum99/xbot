package bot

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Validation constants.
const (
	maxHandleLen         = 15
	maxTriggerKeywordLen = 50
	maxBranchPrefixLen   = 50
	maxAgentCmdLen       = 500
)

var validHandle = regexp.MustCompile(`^[a-zA-Z0-9_]{1,15}$`)

// validTriggerKeyword allows only safe characters in trigger keywords (M8).
var validTriggerKeyword = regexp.MustCompile(`^[a-zA-Z0-9:_ .\-]+$`)

// validBranchPrefix allows only safe characters in branch prefixes (Issue #10).
var validBranchPrefix = regexp.MustCompile(`^[a-zA-Z0-9/_\-\.]+$`)

// BotConfig holds the configuration for the bot.
type BotConfig struct {
	Handle         string        `yaml:"handle"`
	TriggerKeyword string        `yaml:"trigger_keyword"`
	Repo           string        `yaml:"repo"`
	Agent          string        `yaml:"agent"`
	AgentCmd       string        `yaml:"agent_cmd,omitempty"`
	PollInterval   time.Duration `yaml:"poll_interval"`
	BranchPrefix   string        `yaml:"branch_prefix"`
	DryRun         bool          `yaml:"dry_run"`
}

// DefaultConfigPath returns the default path for the bot config file.
// L2: Returns error instead of falling back to current directory.
func DefaultConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(homeDir, ".xbot"), nil
}

// LoadConfig reads the bot config from ~/.xbot.
func LoadConfig() (*BotConfig, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadConfigFromPath(path)
}

// LoadConfigFromPath reads the bot config from the given path.
// Issue #11: Uses single file handle to eliminate TOCTOU between Stat and Read.
func LoadConfigFromPath(path string) (*BotConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("bot not configured – run 'xbot init' first")
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	defer f.Close()

	// L1: Check file permissions on the open handle (no TOCTOU)
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if mode := info.Mode().Perm(); mode&0077 != 0 {
		log.Printf("[WARN] Config file %s has permissive permissions %04o; fixing to 0600", path, mode)
		_ = os.Chmod(path, 0600)
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg BotConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.applyDefaults()
	return &cfg, nil
}

// Save writes the bot config to ~/.xbot.
func (c *BotConfig) Save() error {
	path, err := DefaultConfigPath()
	if err != nil {
		return err
	}
	return c.SaveToPath(path)
}

// SaveToPath writes the bot config to the given path.
func (c *BotConfig) SaveToPath(path string) error {
	c.applyDefaults()

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

func (c *BotConfig) applyDefaults() {
	c.Handle = strings.TrimPrefix(c.Handle, "@")

	if c.TriggerKeyword == "" {
		c.TriggerKeyword = "fix:"
	}
	if c.PollInterval == 0 {
		c.PollInterval = 60 * time.Second
	}
	if c.BranchPrefix == "" {
		c.BranchPrefix = "bot/fix-"
	}
	if c.Agent == "" {
		c.Agent = "claude"
	}
}

// Validate checks the config for security and correctness issues.
func (c *BotConfig) Validate() error {
	// M1: Validate handle format
	if c.Handle == "" {
		return fmt.Errorf("handle is required")
	}
	if !validHandle.MatchString(c.Handle) {
		return fmt.Errorf("invalid handle %q: must be 1-%d alphanumeric/underscore characters", c.Handle, maxHandleLen)
	}

	// M2+M8: Validate trigger keyword — strict allowlist of safe characters
	if len(c.TriggerKeyword) > maxTriggerKeywordLen {
		return fmt.Errorf("trigger keyword too long (max %d characters)", maxTriggerKeywordLen)
	}
	if !validTriggerKeyword.MatchString(c.TriggerKeyword) {
		return fmt.Errorf("trigger keyword contains unsafe characters (allowed: alphanumeric, colon, underscore, space, period, hyphen)")
	}

	// M4: Validate repo path — must be absolute and exist as a directory
	if c.Repo != "" {
		if !filepath.IsAbs(c.Repo) {
			return fmt.Errorf("repo path must be absolute: %s", c.Repo)
		}
		info, err := os.Stat(c.Repo)
		if err != nil {
			return fmt.Errorf("repo path does not exist: %s", c.Repo)
		}
		if !info.IsDir() {
			return fmt.Errorf("repo path is not a directory: %s", c.Repo)
		}
	}

	// L5: Validate branch prefix length
	if len(c.BranchPrefix) > maxBranchPrefixLen {
		return fmt.Errorf("branch prefix too long (max %d characters)", maxBranchPrefixLen)
	}
	// Issue #10: Validate branch prefix characters to prevent prompt injection via branch name
	if c.BranchPrefix != "" && !validBranchPrefix.MatchString(c.BranchPrefix) {
		return fmt.Errorf("branch prefix contains unsafe characters (allowed: alphanumeric, /, _, -, .)")
	}

	// C1/C3: Validate custom agent command — reject all shell metacharacters
	if c.Agent == "custom" {
		if c.AgentCmd == "" {
			return fmt.Errorf("custom agent requires agent_cmd to be set")
		}
		if len(c.AgentCmd) > maxAgentCmdLen {
			return fmt.Errorf("agent_cmd too long (max %d characters)", maxAgentCmdLen)
		}
		// C3: Block all shell metacharacters including <, >, ~, !, #, newlines
		if strings.ContainsAny(c.AgentCmd, ";|&$`\\(){}\"'<>~!#\n\r") {
			return fmt.Errorf("agent_cmd contains unsafe shell metacharacters")
		}
	}

	// Validate agent type
	validAgents := map[string]bool{"claude": true, "codex": true, "gemini": true, "custom": true}
	if !validAgents[strings.ToLower(c.Agent)] {
		return fmt.Errorf("unknown agent type: %s (supported: claude, codex, gemini, custom)", c.Agent)
	}

	return nil
}
