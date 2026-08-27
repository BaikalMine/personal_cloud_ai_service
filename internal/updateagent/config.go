package updateagent

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

type Config struct {
	ListenAddress string          `json:"listen_address"`
	Token         string          `json:"token"`
	LogFile       string          `json:"log_file"`
	Gateway       GitTarget       `json:"gateway"`
	ComfyUI       ComfyTarget     `json:"comfyui"`
	OpenWebUI     OpenWebUITarget `json:"openwebui"`
}

type GitTarget struct {
	WorkingDirectory string `json:"working_directory"`
	RemoteURL        string `json:"remote_url"`
	Branch           string `json:"branch"`
	ComposeFile      string `json:"compose_file"`
	ComposeService   string `json:"compose_service"`
	HealthURL        string `json:"health_url"`
}

type ComfyTarget struct {
	WorkingDirectory string   `json:"working_directory"`
	OutputDirectory  string   `json:"output_directory"`
	RemoteURL        string   `json:"remote_url"`
	Branch           string   `json:"branch"`
	PythonExecutable string   `json:"python_executable"`
	LaunchArguments  []string `json:"launch_arguments"`
	LaunchCommand    []string `json:"launch_command"`
	StopCommand      []string `json:"stop_command"`
	HealthURL        string   `json:"health_url"`
}

type OpenWebUITarget struct {
	ComposeDirectory string `json:"compose_directory"`
	ComposeFile      string `json:"compose_file"`
	ComposeService   string `json:"compose_service"`
	ContainerName    string `json:"container_name"`
	EnvFile          string `json:"env_file"`
	ImageVariable    string `json:"image_variable"`
	ImageRepository  string `json:"image_repository"`
	ReleaseAPI       string `json:"release_api"`
	HealthURL        string `json:"health_url"`
}

func (c *Config) Normalize() error {
	c.ListenAddress = strings.TrimSpace(c.ListenAddress)
	if c.ListenAddress == "" {
		c.ListenAddress = "0.0.0.0:8093"
	}
	c.Token = strings.TrimSpace(c.Token)
	if len(c.Token) < 32 {
		return fmt.Errorf("token must contain at least 32 characters")
	}
	c.LogFile = strings.TrimSpace(c.LogFile)
	if c.LogFile != "" && !filepath.IsAbs(c.LogFile) {
		return fmt.Errorf("log_file path must be absolute")
	}
	if err := c.Gateway.normalize("gateway", true); err != nil {
		return err
	}
	if err := c.ComfyUI.normalize(); err != nil {
		return err
	}
	if err := c.OpenWebUI.normalize(); err != nil {
		return err
	}
	return nil
}

func (t *GitTarget) normalize(name string, required bool) error {
	t.WorkingDirectory = strings.TrimSpace(t.WorkingDirectory)
	t.RemoteURL = strings.TrimSpace(t.RemoteURL)
	t.Branch = strings.TrimSpace(t.Branch)
	t.ComposeFile = strings.TrimSpace(t.ComposeFile)
	t.ComposeService = strings.TrimSpace(t.ComposeService)
	t.HealthURL = strings.TrimSpace(t.HealthURL)
	if !required && t.WorkingDirectory == "" {
		return nil
	}
	if t.WorkingDirectory == "" || t.RemoteURL == "" || t.ComposeFile == "" || t.ComposeService == "" || t.HealthURL == "" {
		return fmt.Errorf("%s update target is incomplete", name)
	}
	if t.Branch == "" {
		t.Branch = "main"
	}
	if _, err := url.ParseRequestURI(t.RemoteURL); err != nil {
		return fmt.Errorf("%s remote_url: %w", name, err)
	}
	if _, err := url.ParseRequestURI(t.HealthURL); err != nil {
		return fmt.Errorf("%s health_url: %w", name, err)
	}
	if !filepath.IsAbs(t.WorkingDirectory) || !filepath.IsAbs(t.ComposeFile) {
		return fmt.Errorf("%s paths must be absolute", name)
	}
	return nil
}

func (t *ComfyTarget) normalize() error {
	t.WorkingDirectory = strings.TrimSpace(t.WorkingDirectory)
	t.OutputDirectory = strings.TrimSpace(t.OutputDirectory)
	t.RemoteURL = strings.TrimSpace(t.RemoteURL)
	t.Branch = strings.TrimSpace(t.Branch)
	t.PythonExecutable = strings.TrimSpace(t.PythonExecutable)
	t.HealthURL = strings.TrimSpace(t.HealthURL)
	if t.WorkingDirectory == "" || t.RemoteURL == "" || t.PythonExecutable == "" || t.HealthURL == "" || len(t.LaunchArguments) == 0 || len(t.LaunchCommand) == 0 || len(t.StopCommand) == 0 {
		return fmt.Errorf("comfyui update target is incomplete")
	}
	if t.Branch == "" {
		t.Branch = "master"
	}
	if !filepath.IsAbs(t.WorkingDirectory) || !filepath.IsAbs(t.PythonExecutable) {
		return fmt.Errorf("comfyui paths must be absolute")
	}
	if t.OutputDirectory != "" && !filepath.IsAbs(t.OutputDirectory) {
		return fmt.Errorf("comfyui output_directory path must be absolute")
	}
	if _, err := url.ParseRequestURI(t.RemoteURL); err != nil {
		return fmt.Errorf("comfyui remote_url: %w", err)
	}
	if _, err := url.ParseRequestURI(t.HealthURL); err != nil {
		return fmt.Errorf("comfyui health_url: %w", err)
	}
	return nil
}

func (t *OpenWebUITarget) normalize() error {
	t.ComposeDirectory = strings.TrimSpace(t.ComposeDirectory)
	t.ComposeFile = strings.TrimSpace(t.ComposeFile)
	t.ComposeService = strings.TrimSpace(t.ComposeService)
	t.ContainerName = strings.TrimSpace(t.ContainerName)
	t.EnvFile = strings.TrimSpace(t.EnvFile)
	t.ImageVariable = strings.TrimSpace(t.ImageVariable)
	t.ImageRepository = strings.TrimSpace(t.ImageRepository)
	t.ReleaseAPI = strings.TrimSpace(t.ReleaseAPI)
	t.HealthURL = strings.TrimSpace(t.HealthURL)
	if t.ComposeDirectory == "" || t.ComposeFile == "" || t.ComposeService == "" || t.ContainerName == "" || t.EnvFile == "" || t.ImageVariable == "" || t.ImageRepository == "" || t.ReleaseAPI == "" || t.HealthURL == "" {
		return fmt.Errorf("openwebui update target is incomplete")
	}
	for name, raw := range map[string]string{"release_api": t.ReleaseAPI, "health_url": t.HealthURL} {
		if _, err := url.ParseRequestURI(raw); err != nil {
			return fmt.Errorf("openwebui %s: %w", name, err)
		}
	}
	for _, path := range []string{t.ComposeDirectory, t.ComposeFile, t.EnvFile} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("openwebui paths must be absolute")
		}
	}
	return nil
}
