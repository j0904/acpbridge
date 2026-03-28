package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config represents the bridge configuration
type Config struct {
	Listen       string        `json:"listen"`
	APIKey       string        `json:"api_key"`
	CLI          CLICfg        `json:"cli"`
	Model        string        `json:"model"`
	CORS         CORSConfig    `json:"cors,omitempty"`
	AllowOrigins []string      `json:"allow_origins,omitempty"`
}

// CLICfg represents CLI configuration
type CLICfg struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Workspace string   `json:"workspace"`
}

// CORSConfig represents CORS configuration
type CORSConfig struct {
	AllowedOrigins []string `json:"allowed_origins"`
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Listen: ":9090",
		APIKey: "",
		CLI: CLICfg{
			Command:   "qwen",
			Args:      []string{"--approval-mode=yolo"},
			Workspace: "~/.picoclaw/qwen-ws",
		},
		Model: "qwen-cli/qwen-max",
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
		},
	}
}

// Load loads configuration from a JSON file
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	if path == "" {
		// Try default locations
		paths := []string{
			"config.json",
			"bridge-acp.json",
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Expand workspace path
	cfg.CLI.Workspace = expandTilde(cfg.CLI.Workspace)

	return cfg, nil
}

// expandTilde expands ~ to home directory
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if runtime.GOOS == "windows" {
			path = strings.Replace(path, "~", home, 1)
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
