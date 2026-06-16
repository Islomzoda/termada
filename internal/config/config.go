// Package config loads and represents the termada configuration
// (~/.config/termada/config.yaml, spec §24). Credentials live separately in the
// encrypted vault, never here.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration.
type Config struct {
	AgentTransport string                  `yaml:"agent_transport"`
	HTTP           HTTPConfig              `yaml:"http"`
	Dashboard      DashboardConfig         `yaml:"dashboard"`
	Notifications  NotificationsConfig     `yaml:"notifications"`
	Defaults       Defaults                `yaml:"defaults"`
	TimeoutClasses map[string]int          `yaml:"timeout_classes"`
	Vault          VaultConfig             `yaml:"vault"`
	Agents         []AgentConfig           `yaml:"agents"`
	Servers        []ServerConfig          `yaml:"servers"`
	Policies       map[string]PolicyConfig `yaml:"policies"`
	Redaction      []string                `yaml:"redaction"`
	Recipes        map[string]RecipeConfig `yaml:"recipes"`
}

type HTTPConfig struct {
	Bind string `yaml:"bind"`
}

type DashboardConfig struct {
	Enabled     bool   `yaml:"enabled"`
	OpenBrowser bool   `yaml:"open_browser"`
	Socket      string `yaml:"socket"` // uds | tcp
}

type NotificationsConfig struct {
	Desktop  bool           `yaml:"desktop"`
	Telegram TelegramConfig `yaml:"telegram"`
}

type TelegramConfig struct {
	Enabled        bool    `yaml:"enabled"`
	BotToken       string  `yaml:"bot_token"`
	ChatID         string  `yaml:"chat_id"`
	AllowedUserIDs []int64 `yaml:"allowed_user_ids"`
}

type Defaults struct {
	TimeoutMS            int  `yaml:"timeout_ms"`
	MaxOutputBytes       int  `yaml:"max_output_bytes"`
	OutputRetentionBytes int  `yaml:"output_retention_bytes"`
	StripANSI            bool `yaml:"strip_ansi"`
	PTYCols              int  `yaml:"pty_cols"`
	MaxForegroundJobs    int  `yaml:"max_foreground_jobs"`
	MaxBackgroundJobs    int  `yaml:"max_background_jobs"`
	MaxJobsPerAgent      int  `yaml:"max_jobs_per_agent"`
	SilenceKillMS        int  `yaml:"silence_kill_ms"`
	InputTimeoutMS       int  `yaml:"input_timeout_ms"`
	ConfirmTimeoutMS     int  `yaml:"confirm_timeout_ms"`
}

type VaultConfig struct {
	Backend      string `yaml:"backend"` // encrypted_file | keychain
	File         string `yaml:"file"`
	UnlockTTLMS  int    `yaml:"unlock_ttl_ms"`
	IdleRelockMS int    `yaml:"idle_relock_ms"`
}

type AgentConfig struct {
	ID     string `yaml:"id"`
	Policy string `yaml:"policy"`
	Token  string `yaml:"token"` // optional: binds this agent id to a token (non-spoofable identity)
}

type ServerConfig struct {
	Name string   `yaml:"name"`
	Host string   `yaml:"host"`
	Port int      `yaml:"port"`
	User string   `yaml:"user"`
	Auth string   `yaml:"auth"` // vault entry reference
	Tags []string `yaml:"tags"`
	Tmux string   `yaml:"tmux"` // auto | require | off
}

type PolicyConfig struct {
	Allow      []string         `yaml:"allow"`
	Deny       []string         `yaml:"deny"`
	Confirm    []string         `yaml:"confirm"`
	AutoAnswer []AutoAnswerRule `yaml:"auto_answer"`
}

type AutoAnswerRule struct {
	Match string `yaml:"match"`
	Send  string `yaml:"send"`
}

type RecipeConfig struct {
	Target string     `yaml:"target"`
	Steps  [][]string `yaml:"steps"`
}

// Default returns a configuration with sensible defaults (spec §24).
func Default() Config {
	return Config{
		AgentTransport: "stdio",
		HTTP:           HTTPConfig{Bind: "127.0.0.1:7717"}, // not 7000: macOS AirPlay Receiver squats on :7000
		Dashboard:      DashboardConfig{Enabled: true, OpenBrowser: false, Socket: "tcp"},
		Notifications:  NotificationsConfig{Desktop: true},
		Defaults: Defaults{
			TimeoutMS:            30000,
			MaxOutputBytes:       100000,
			OutputRetentionBytes: 5000000,
			StripANSI:            true,
			PTYCols:              200,
			MaxForegroundJobs:    8,
			MaxBackgroundJobs:    32,
			ConfirmTimeoutMS:     120000,
		},
		TimeoutClasses: map[string]int{
			"build": 1800000, "install": 600000, "test": 1800000,
			"db": 0, "network": 120000, "default": 30000,
		},
		Vault:    VaultConfig{Backend: "encrypted_file", File: "~/.config/termada/vault.age"},
		Policies: map[string]PolicyConfig{},
		Recipes:  map[string]RecipeConfig{},
	}
}

// DefaultPath is the standard config location.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "termada", "config.yaml")
}

// Load reads config from path, applying defaults for anything unset. A missing
// file is not an error: defaults are returned.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	cfg.applyDefaults()
	return cfg, nil
}

// applyDefaults fills zero-valued required fields after unmarshal.
func (c *Config) applyDefaults() {
	d := &c.Defaults
	if d.TimeoutMS == 0 {
		d.TimeoutMS = 30000
	}
	if d.OutputRetentionBytes == 0 {
		d.OutputRetentionBytes = 5000000
	}
	if d.PTYCols == 0 {
		d.PTYCols = 200
	}
	if d.MaxForegroundJobs == 0 {
		d.MaxForegroundJobs = 8
	}
	if d.ConfirmTimeoutMS == 0 {
		d.ConfirmTimeoutMS = 120000
	}
	if c.Vault.Backend == "" {
		c.Vault.Backend = "encrypted_file"
	}
	if c.AgentTransport == "" {
		c.AgentTransport = "stdio"
	}
}

// ExpandPath expands a leading ~ to the user's home directory.
func ExpandPath(p string) string {
	if p == "~" || (len(p) >= 2 && p[:2] == "~/") {
		home, _ := os.UserHomeDir()
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

// PolicyFor returns the policy name configured for an agent id (empty if none).
func (c *Config) PolicyFor(agentID string) string {
	for _, a := range c.Agents {
		if a.ID == agentID {
			return a.Policy
		}
	}
	return ""
}
