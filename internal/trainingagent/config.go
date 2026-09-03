package trainingagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ai-access-gateway/internal/loratraining"
)

const defaultMaxDatasetBytes int64 = 512 << 20

var profileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,95}$`)

type ProfileConfig struct {
	ID             string `json:"id"`
	Family         string `json:"family"`
	Name           string `json:"name"`
	BaseModel      string `json:"base_model"`
	Description    string `json:"description"`
	DiT            string `json:"dit"`
	VAE            string `json:"vae"`
	TextEncoder    string `json:"text_encoder"`
	ModelVersion   string `json:"model_version,omitempty"`
	StripPrefix    string `json:"strip_prefix,omitempty"`
	BlocksToSwap   int    `json:"blocks_to_swap,omitempty"`
	FP8Base        bool   `json:"fp8_base,omitempty"`
	FP8TextEncoder bool   `json:"fp8_text_encoder,omitempty"`
}

func (profile ProfileConfig) Public(config Config) loratraining.Profile {
	detail := profile.Readiness(config)
	return loratraining.Profile{
		ID: profile.ID, Family: profile.Family, Name: profile.Name, BaseModel: profile.BaseModel,
		Description: profile.Description, Ready: detail == "", Detail: detail,
	}
}

func (profile ProfileConfig) Readiness(config Config) string {
	checks := []struct {
		label string
		path  string
		dir   bool
	}{
		{"Python", config.PythonExe, false},
		{"Musubi Tuner", config.TunerDir, true},
		{"базовая модель", profile.DiT, false},
		{"VAE", profile.VAE, false},
		{"text encoder", profile.TextEncoder, false},
		{"папка LoRA ComfyUI", config.ComfyLoraDir, true},
	}
	for _, check := range checks {
		info, err := os.Stat(check.path)
		if err != nil {
			return fmt.Sprintf("Не найден %s: %s", check.label, check.path)
		}
		if check.dir != info.IsDir() {
			return fmt.Sprintf("Некорректный путь %s: %s", check.label, check.path)
		}
	}
	return ""
}

type Config struct {
	Addr            string          `json:"listen_address"`
	Token           string          `json:"token"`
	RootDir         string          `json:"root_directory"`
	TunerDir        string          `json:"tuner_directory"`
	PythonExe       string          `json:"python_executable"`
	ComfyLoraDir    string          `json:"comfyui_lora_directory"`
	LogFile         string          `json:"log_file,omitempty"`
	Profiles        []ProfileConfig `json:"profiles"`
	MaxDatasetBytes int64           `json:"max_dataset_bytes,omitempty"`
}

func LoadConfig() (Config, error) {
	config := Config{
		Addr:            env("LORA_TRAINING_AGENT_ADDR", "127.0.0.1:8095"),
		Token:           strings.TrimSpace(os.Getenv("LORA_TRAINING_AGENT_TOKEN")),
		RootDir:         filepath.Clean(env("LORA_TRAINING_AGENT_ROOT", `C:\Work\ComfyUI\tools\lora-training-agent-data`)),
		TunerDir:        filepath.Clean(env("MUSUBI_TUNER_DIR", `C:\Work\ComfyUI\tools\musubi-tuner`)),
		PythonExe:       filepath.Clean(strings.TrimSpace(os.Getenv("MUSUBI_PYTHON_EXE"))),
		ComfyLoraDir:    filepath.Clean(env("COMFYUI_LORA_DIR", `C:\Work\ComfyUI\models\loras`)),
		MaxDatasetBytes: defaultMaxDatasetBytes,
	}
	profileFile := filepath.Clean(strings.TrimSpace(os.Getenv("LORA_TRAINING_PROFILES_FILE")))
	if profileFile == "." || profileFile == "" {
		return Config{}, errors.New("LORA_TRAINING_PROFILES_FILE is required")
	}
	payload, err := os.ReadFile(profileFile)
	if err != nil {
		return Config{}, fmt.Errorf("read training profiles: %w", err)
	}
	if err := json.Unmarshal(payload, &config.Profiles); err != nil {
		return Config{}, fmt.Errorf("decode training profiles: %w", err)
	}
	return normalizeConfig(config)
}

func LoadConfigFile(filename string) (Config, error) {
	payload, err := os.ReadFile(filepath.Clean(filename))
	if err != nil {
		return Config{}, fmt.Errorf("read LoRA training agent config: %w", err)
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode LoRA training agent config: %w", err)
	}
	return normalizeConfig(config)
}

func normalizeConfig(config Config) (Config, error) {
	config.Token = strings.TrimSpace(config.Token)
	config.Addr = strings.TrimSpace(config.Addr)
	if config.Addr == "" {
		config.Addr = "127.0.0.1:8095"
	}
	config.RootDir = cleanConfigPath(config.RootDir, `C:\Work\ComfyUI\tools\lora-training-agent-data`)
	config.TunerDir = cleanConfigPath(config.TunerDir, `C:\Work\ComfyUI\tools\musubi-tuner`)
	config.PythonExe = filepath.Clean(strings.TrimSpace(config.PythonExe))
	config.ComfyLoraDir = cleanConfigPath(config.ComfyLoraDir, `C:\Work\ComfyUI\models\loras`)
	if strings.TrimSpace(config.LogFile) != "" {
		config.LogFile = filepath.Clean(config.LogFile)
	}
	if config.MaxDatasetBytes <= 0 {
		config.MaxDatasetBytes = defaultMaxDatasetBytes
	}
	if len(config.Token) < 32 || len(config.Token) > 512 {
		return Config{}, errors.New("LoRA training agent token must contain 32-512 characters")
	}
	if config.PythonExe == "." || config.PythonExe == "" {
		return Config{}, errors.New("Musubi Python executable is required")
	}
	if config.MaxDatasetBytes > 2<<30 {
		return Config{}, errors.New("max_dataset_bytes must not exceed 2 GiB")
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func cleanConfigPath(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		value = fallback
	}
	return filepath.Clean(value)
}

func (config Config) Validate() error {
	if len(config.Profiles) == 0 {
		return errors.New("at least one image training profile is required")
	}
	seen := make(map[string]struct{}, len(config.Profiles))
	for index, profile := range config.Profiles {
		if !profileIDPattern.MatchString(profile.ID) {
			return fmt.Errorf("profile %d has an invalid id", index+1)
		}
		if profile.Family != "krea2" && profile.Family != "flux2-klein" {
			return fmt.Errorf("profile %s uses unsupported family %q", profile.ID, profile.Family)
		}
		if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.BaseModel) == "" {
			return fmt.Errorf("profile %s must include name and base_model", profile.ID)
		}
		if _, duplicate := seen[profile.ID]; duplicate {
			return fmt.Errorf("duplicate profile id %q", profile.ID)
		}
		seen[profile.ID] = struct{}{}
		if profile.Family == "flux2-klein" && profile.ModelVersion != "klein-base-4b" && profile.ModelVersion != "klein-base-9b" {
			return fmt.Errorf("profile %s must use a trainable Flux.2 Klein base model", profile.ID)
		}
		if profile.BlocksToSwap < 0 || profile.BlocksToSwap > 26 {
			return fmt.Errorf("profile %s has invalid blocks_to_swap", profile.ID)
		}
	}
	return nil
}

func (config Config) Profile(id string) (ProfileConfig, bool) {
	for _, profile := range config.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return ProfileConfig{}, false
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
