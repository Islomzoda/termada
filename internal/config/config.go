// Package config loads and represents the termada configuration
// (~/.config/termada/config.yaml, spec §24). Credentials live separately in the
// encrypted vault, never here.
package config

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxConfigBytes          = 8 << 20
	maxOutputPageBytes      = 1 << 20
	maxOutputRetentionBytes = 16 << 20
	maxConfiguredItems      = 1024
	maxConfiguredRecipes    = 256
	maxRecipeSteps          = 256
	maxRecipeCommandArgs    = 4096
	maxRecipeCommandBytes   = 256 << 10
	maxAllRecipeBytes       = 512 << 10
	maxConfigTimeoutMS      = 24 * 60 * 60 * 1000
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
	Security       SecurityConfig          `yaml:"security"`
}

// SecurityConfig holds host-level guard rails.
type SecurityConfig struct {
	// ProtectedPaths are extra paths that file_read/file_write must refuse, on top
	// of the always-protected defaults (the daemon runtime dir with its tokens and
	// vault, plus ~/.ssh, ~/.aws, ~/.gnupg). Supports ~ expansion. (spec C2/FS-3)
	ProtectedPaths []string `yaml:"protected_paths"`
	// RunAs drops local agent sessions to a less-privileged user so their `exec`
	// can't read the daemon's secrets, the control socket, or host credential
	// stores (SEC-8). Either a username ("termada-agent") or a numeric "uid:gid".
	// Empty = run as the daemon (default). REQUIRES the daemon to run as root, and
	// the agent uid needs access (group/ACL) to the working directories it edits.
	// Dropped shells receive a minimal environment rather than daemon credentials.
	RunAs string `yaml:"run_as"`
}

type HTTPConfig struct {
	Bind string `yaml:"bind"`
}

type DashboardConfig struct {
	Enabled     bool   `yaml:"enabled"`
	OpenBrowser bool   `yaml:"open_browser"`
	Socket      string `yaml:"socket"` // compatibility field; only tcp is supported
	// LocalTrust is retained only so older configuration files continue to load.
	// It no longer changes authorization: every TCP API request requires the
	// dashboard token. Static assets remain public on the loopback origin so the
	// UI can render its token prompt.
	LocalTrust *bool `yaml:"local_trust"`
}

type NotificationsConfig struct {
	Desktop  bool           `yaml:"desktop"`
	Telegram TelegramConfig `yaml:"telegram"`
}

type TelegramConfig struct {
	Enabled        bool    `yaml:"enabled"`
	BotToken       string  `yaml:"bot_token"`
	ChatID         string  `yaml:"chat_id"`
	AllowedUserIDs []int64 `yaml:"allowed_user_ids"` // compatibility field; outbound integration has no users
}

type Defaults struct {
	TimeoutMS            int `yaml:"timeout_ms"`
	MaxOutputBytes       int `yaml:"max_output_bytes"`
	OutputRetentionBytes int `yaml:"output_retention_bytes"`
	PTYCols              int `yaml:"pty_cols"`
	MaxForegroundJobs    int `yaml:"max_foreground_jobs"`
	MaxBackgroundJobs    int `yaml:"max_background_jobs"` // separate cap for background/auto-backgrounded jobs; no longer counted against max_foreground_jobs
	MaxJobsPerAgent      int `yaml:"max_jobs_per_agent"`
	MaxJobRuntimeMS      int `yaml:"max_job_runtime_ms"` // 0 = no cap; reap (SIGKILL) jobs running longer (runaway/hung safety net)
	ConfirmTimeoutMS     int `yaml:"confirm_timeout_ms"`
}

type VaultConfig struct {
	Backend      string `yaml:"backend"` // encrypted_file is the only supported backend
	File         string `yaml:"file"`
	UnlockTTLMS  int    `yaml:"unlock_ttl_ms"` // compatibility field; use idle_relock_ms
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
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return cfg, err
	}
	if len(data) > maxConfigBytes {
		return cfg, fmt.Errorf("config exceeds %d byte limit", maxConfigBytes)
	}
	data, err = expandEnvRefs(data)
	if err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, err
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvRefs expands explicit ${NAME} references in parsed YAML scalar nodes.
// Expanding after parsing keeps environment values as data: characters such as
// ':', '#', and newlines cannot inject sibling YAML fields. Missing variables
// are an error instead of silently becoming empty credentials.
func expandEnvRefs(data []byte) ([]byte, error) {
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		if err == io.EOF {
			return []byte("{}\n"), nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("config must contain exactly one YAML document")
	}
	missing := []string{}
	seen := map[string]bool{}
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			node.Value = envRef.ReplaceAllStringFunc(node.Value, func(ref string) string {
				name := envRef.FindStringSubmatch(ref)[1]
				if value, ok := os.LookupEnv(name); ok {
					return value
				}
				if !seen[name] {
					seen[name] = true
					missing = append(missing, name)
				}
				return ref
			})
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(&doc)
	if len(missing) > 0 {
		return nil, fmt.Errorf("config references unset environment variable(s): %s", strings.Join(missing, ", "))
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (c *Config) validate() error {
	if c.AgentTransport != "stdio" {
		return fmt.Errorf("agent_transport %q is not supported; use stdio", c.AgentTransport)
	}
	if c.Dashboard.Socket != "" && c.Dashboard.Socket != "tcp" {
		return fmt.Errorf("dashboard.socket %q is not supported; use tcp", c.Dashboard.Socket)
	}
	if c.Vault.Backend != "encrypted_file" {
		return fmt.Errorf("vault.backend %q is not supported; use encrypted_file", c.Vault.Backend)
	}
	if c.Vault.UnlockTTLMS != 0 {
		return fmt.Errorf("vault.unlock_ttl_ms is not supported; use idle_relock_ms")
	}
	if len(c.Notifications.Telegram.AllowedUserIDs) > 0 {
		return fmt.Errorf("notifications.telegram.allowed_user_ids is not supported: Telegram is outbound-only")
	}
	if c.Notifications.Telegram.Enabled && (c.Notifications.Telegram.BotToken == "" || c.Notifications.Telegram.ChatID == "") {
		return fmt.Errorf("notifications.telegram requires bot_token and chat_id when enabled")
	}
	if c.HTTP.Bind == "" {
		return fmt.Errorf("http.bind must not be empty")
	}
	_, portText, err := net.SplitHostPort(c.HTTP.Bind)
	if err != nil {
		return fmt.Errorf("http.bind must be host:port: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("http.bind port must be in 1..65535")
	}

	d := c.Defaults
	if d.TimeoutMS < 0 || d.MaxOutputBytes < 0 || d.OutputRetentionBytes < 0 || d.PTYCols < 0 ||
		d.MaxForegroundJobs < 0 || d.MaxBackgroundJobs < 0 || d.MaxJobsPerAgent < 0 ||
		d.MaxJobRuntimeMS < 0 || d.ConfirmTimeoutMS < 0 {
		return fmt.Errorf("defaults values must not be negative")
	}
	if d.MaxOutputBytes > maxOutputPageBytes || d.OutputRetentionBytes > maxOutputRetentionBytes {
		return fmt.Errorf("defaults output limits exceed max_output_bytes=%d or output_retention_bytes=%d", maxOutputPageBytes, maxOutputRetentionBytes)
	}
	if d.PTYCols > 1000 || d.MaxForegroundJobs > 128 || d.MaxBackgroundJobs > 128 || d.MaxJobsPerAgent > 128 {
		return fmt.Errorf("defaults PTY/job limits exceed supported bounds")
	}
	if d.TimeoutMS > maxConfigTimeoutMS || d.MaxJobRuntimeMS > maxConfigTimeoutMS || d.ConfirmTimeoutMS > maxConfigTimeoutMS {
		return fmt.Errorf("defaults timeouts must not exceed %d ms", maxConfigTimeoutMS)
	}
	if c.Vault.IdleRelockMS < 0 || c.Vault.IdleRelockMS > maxConfigTimeoutMS {
		return fmt.Errorf("vault.idle_relock_ms must be in 0..%d", maxConfigTimeoutMS)
	}
	for name, ms := range c.TimeoutClasses {
		if ms < 0 || ms > maxConfigTimeoutMS {
			return fmt.Errorf("timeout_classes.%s must be in 0..%d", name, maxConfigTimeoutMS)
		}
	}
	if len(c.Agents) > maxConfiguredItems || len(c.Servers) > maxConfiguredItems || len(c.Policies) > maxConfiguredItems {
		return fmt.Errorf("config contains too many agents, servers or policies (max %d each)", maxConfiguredItems)
	}
	if len(c.Recipes) > maxConfiguredRecipes || len(c.Redaction) > 256 || len(c.Security.ProtectedPaths) > 256 {
		return fmt.Errorf("config contains too many recipes, redaction rules or protected paths")
	}

	ids := map[string]bool{}
	tokens := map[string]bool{}
	for _, a := range c.Agents {
		if strings.TrimSpace(a.ID) == "" {
			return fmt.Errorf("agents[].id must not be empty")
		}
		if a.ID != strings.TrimSpace(a.ID) {
			return fmt.Errorf("agent id %q must not have leading or trailing whitespace", a.ID)
		}
		if len(a.ID) > 128 || strings.IndexFunc(a.ID, func(r rune) bool { return r < 0x21 || r == 0x7f }) >= 0 {
			return fmt.Errorf("agent id %q must be at most 128 bytes and contain no whitespace/control characters", a.ID)
		}
		if ids[a.ID] {
			return fmt.Errorf("duplicate agent id %q", a.ID)
		}
		ids[a.ID] = true
		if a.Policy != "" {
			if _, ok := c.Policies[a.Policy]; !ok {
				return fmt.Errorf("agent %q references unknown policy %q", a.ID, a.Policy)
			}
		}
		if a.Token != "" {
			if len(a.Token) > 4096 || strings.IndexFunc(a.Token, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
				return fmt.Errorf("token for agent %q must be at most 4096 visible ASCII characters", a.ID)
			}
			if len(a.Token) < 16 {
				return fmt.Errorf("token for agent %q must be at least 16 characters", a.ID)
			}
			if tokens[a.Token] {
				return fmt.Errorf("duplicate agent token configured for %q", a.ID)
			}
			tokens[a.Token] = true
		}
	}
	serverNames := map[string]bool{}
	for _, server := range c.Servers {
		if strings.TrimSpace(server.Name) == "" || strings.TrimSpace(server.Host) == "" || strings.TrimSpace(server.User) == "" {
			return fmt.Errorf("servers[] requires non-empty name, host and user")
		}
		if len(server.Name) > 255 || strings.IndexFunc(server.Name, func(r rune) bool { return r < 0x21 || r == 0x7f }) >= 0 {
			return fmt.Errorf("server name %q must be at most 255 bytes and contain no whitespace/control characters", server.Name)
		}
		if serverNames[server.Name] {
			return fmt.Errorf("duplicate server name %q", server.Name)
		}
		serverNames[server.Name] = true
		if server.Port < 0 || server.Port > 65535 {
			return fmt.Errorf("server %q port must be 0 (default SSH) or in 1..65535", server.Name)
		}
		if len(server.Host) > 1024 || len(server.User) > 255 || len(server.Auth) > 1024 || len(server.Tags) > 64 {
			return fmt.Errorf("server %q contains oversized fields or too many tags", server.Name)
		}
		for _, tag := range server.Tags {
			if strings.TrimSpace(tag) == "" || len(tag) > 128 || strings.IndexFunc(tag, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
				return fmt.Errorf("server %q contains an invalid tag", server.Name)
			}
		}
	}
	allRecipeBytes := 0
	for name, recipe := range c.Recipes {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("recipe name must not be empty")
		}
		if len(recipe.Steps) == 0 {
			return fmt.Errorf("recipe %q must contain at least one step", name)
		}
		if len(name) > 255 || len(recipe.Steps) > maxRecipeSteps {
			return fmt.Errorf("recipe %q name/step count exceeds supported bounds", name)
		}
		if recipe.Target != "" && recipe.Target != "local" && !serverNames[recipe.Target] {
			return fmt.Errorf("recipe %q references unknown target %q", name, recipe.Target)
		}
		for i, step := range recipe.Steps {
			if len(step) == 0 || strings.TrimSpace(step[0]) == "" {
				return fmt.Errorf("recipe %q step %d must contain a command", name, i)
			}
			if len(step) > maxRecipeCommandArgs {
				return fmt.Errorf("recipe %q step %d has too many arguments", name, i)
			}
			total := 0
			for _, arg := range step {
				if len(arg) > maxRecipeCommandBytes-total {
					return fmt.Errorf("recipe %q step %d exceeds %d byte argv limit", name, i, maxRecipeCommandBytes)
				}
				total += len(arg)
				allRecipeBytes += len(arg)
				if allRecipeBytes > maxAllRecipeBytes {
					return fmt.Errorf("recipe commands exceed %d byte aggregate limit", maxAllRecipeBytes)
				}
			}
		}
	}
	for i, pattern := range c.Redaction {
		if len(pattern) > 4096 {
			return fmt.Errorf("redaction[%d] exceeds 4096 byte limit", i)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("redaction[%d]: %w", i, err)
		}
	}
	for i, path := range c.Security.ProtectedPaths {
		if len(path) > 4096 {
			return fmt.Errorf("security.protected_paths[%d] exceeds 4096 byte limit", i)
		}
	}
	return nil
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
