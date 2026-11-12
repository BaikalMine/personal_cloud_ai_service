package main

import (
	"errors"
	"net"
	"strconv"
	"strings"

	"ai-access-gateway/internal/inference"
)

func ollamaAPISettings(mode string, env func(string) string) (string, inference.OllamaAPIConfig, error) {
	address := strings.TrimSpace(env("INFERENCE_OLLAMA_ADDR"))
	config := inference.OllamaAPIConfig{
		ClientKey:    strings.TrimSpace(env("INFERENCE_OLLAMA_CLIENT_KEY")),
		AdmissionURL: strings.TrimSpace(env("INFERENCE_OLLAMA_ADMISSION_URL")),
		AdmissionKey: strings.TrimSpace(env("INFERENCE_OLLAMA_ADMISSION_KEY")),
	}
	if address == "" {
		if config != (inference.OllamaAPIConfig{}) {
			return "", config, errors.New("Ollama API settings require INFERENCE_OLLAMA_ADDR")
		}
		return "", config, nil
	}
	if mode != "windows-owned" {
		return "", config, errors.New("Ollama API requires INFERENCE_RUNTIME=windows-owned")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", config, errors.New("INFERENCE_OLLAMA_ADDR must be an explicit local IP and port")
	}
	ip := net.ParseIP(host)
	number, err := strconv.Atoi(port)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) || err != nil || number < 1024 || number > 65535 {
		return "", config, errors.New("INFERENCE_OLLAMA_ADDR requires an explicit loopback/private IP and port 1024-65535")
	}
	if address == strings.TrimSpace(env("INFERENCE_MODEL_ADDR")) {
		return "", config, errors.New("Ollama API listener and native model listener must be separate")
	}
	if err := inference.ValidateOllamaAPIConfig(config); err != nil {
		return "", config, err
	}
	return address, config, nil
}
