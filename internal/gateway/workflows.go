package gateway

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net/http"
	"strconv"
	"strings"
)

//go:embed workflows/*.json
var workflowFS embed.FS

type workflowDefinition struct {
	ID            string                    `json:"id"`
	Name          string                    `json:"name"`
	Description   string                    `json:"description"`
	RequiresImage bool                      `json:"requires_image"`
	Nodes         map[string]map[string]any `json:"nodes"`
}

type workflowView struct {
	ID            string
	Name          string
	Description   string
	RequiresImage bool
}

type generationForm struct {
	TemplateID string
	Checkpoint string
	InputImage string
	Positive   string
	Negative   string
	Width      int
	Height     int
	Steps      int
	CFG        float64
	Denoise    float64
	Seed       int64
}

func loadWorkflowDefinitions() ([]workflowDefinition, error) {
	paths, err := fs.Glob(workflowFS, "workflows/*.json")
	if err != nil {
		return nil, err
	}
	definitions := make([]workflowDefinition, 0, len(paths))
	for _, path := range paths {
		body, err := workflowFS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var definition workflowDefinition
		if err := json.Unmarshal(body, &definition); err != nil {
			return nil, fmt.Errorf("workflow %s: %w", path, err)
		}
		if definition.ID == "" || definition.Name == "" || len(definition.Nodes) == 0 {
			return nil, fmt.Errorf("workflow %s is incomplete", path)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func findWorkflow(definitions []workflowDefinition, id string) (workflowDefinition, bool) {
	for _, definition := range definitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return workflowDefinition{}, false
}

func (definition workflowDefinition) buildPrompt(input generationForm) (map[string]any, error) {
	if definition.RequiresImage && strings.TrimSpace(input.InputImage) == "" {
		return nil, errors.New("для этого workflow нужно добавить фото")
	}
	if strings.TrimSpace(input.Checkpoint) == "" {
		return nil, errors.New("выберите модель checkpoint")
	}
	if strings.TrimSpace(input.Positive) == "" {
		return nil, errors.New("добавьте позитивный промт")
	}
	if len(input.Positive) > 4000 || len(input.Negative) > 4000 {
		return nil, errors.New("промт слишком длинный")
	}
	if input.Width < 256 || input.Width > 2048 || input.Width%64 != 0 || input.Height < 256 || input.Height > 2048 || input.Height%64 != 0 {
		return nil, errors.New("ширина и высота должны быть от 256 до 2048 и кратны 64")
	}
	if input.Steps < 1 || input.Steps > 100 {
		return nil, errors.New("число шагов должно быть от 1 до 100")
	}
	if input.CFG < 1 || input.CFG > 30 {
		return nil, errors.New("CFG должен быть от 1 до 30")
	}
	if input.Denoise < 0.05 || input.Denoise > 1 {
		return nil, errors.New("сила изменения должна быть от 0.05 до 1")
	}
	if input.Seed < 0 {
		seed, err := randomSeed()
		if err != nil {
			return nil, err
		}
		input.Seed = seed
	}
	cloned, err := cloneWorkflowNodes(definition.Nodes)
	if err != nil {
		return nil, err
	}
	values := map[string]any{
		"checkpoint":      input.Checkpoint,
		"input_image":     input.InputImage,
		"positive_prompt": input.Positive,
		"negative_prompt": input.Negative,
		"width":           input.Width,
		"height":          input.Height,
		"steps":           input.Steps,
		"cfg":             input.CFG,
		"denoise":         input.Denoise,
		"seed":            input.Seed,
	}
	for nodeID, node := range cloned {
		cloned[nodeID] = replaceWorkflowValues(node, values).(map[string]any)
	}
	prompt := make(map[string]any, len(cloned))
	for nodeID, node := range cloned {
		prompt[nodeID] = node
	}
	return prompt, nil
}

func cloneWorkflowNodes(nodes map[string]map[string]any) (map[string]map[string]any, error) {
	body, err := json.Marshal(nodes)
	if err != nil {
		return nil, err
	}
	var cloned map[string]map[string]any
	if err := json.Unmarshal(body, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func replaceWorkflowValues(value any, values map[string]any) any {
	switch typed := value.(type) {
	case string:
		if strings.HasPrefix(typed, "{{") && strings.HasSuffix(typed, "}}") {
			if replacement, ok := values[strings.TrimSpace(typed[2:len(typed)-2])]; ok {
				return replacement
			}
		}
		result := typed
		for key, replacement := range values {
			result = strings.ReplaceAll(result, "{{"+key+"}}", fmt.Sprint(replacement))
		}
		return result
	case map[string]any:
		for key, item := range typed {
			typed[key] = replaceWorkflowValues(item, values)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = replaceWorkflowValues(item, values)
		}
		return typed
	default:
		return value
	}
}

func randomSeed() (int64, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return 0, err
	}
	return value.Int64(), nil
}

func parseGenerationForm(r *http.Request) (generationForm, error) {
	if err := r.ParseForm(); err != nil {
		return generationForm{}, err
	}
	parseInt := func(name string, fallback int) (int, error) {
		value := strings.TrimSpace(r.Form.Get(name))
		if value == "" {
			return fallback, nil
		}
		return strconv.Atoi(value)
	}
	parseFloat := func(name string, fallback float64) (float64, error) {
		value := strings.TrimSpace(r.Form.Get(name))
		if value == "" {
			return fallback, nil
		}
		return strconv.ParseFloat(value, 64)
	}
	width, err := parseInt("width", 1024)
	if err != nil {
		return generationForm{}, errors.New("некорректная ширина")
	}
	height, err := parseInt("height", 1024)
	if err != nil {
		return generationForm{}, errors.New("некорректная высота")
	}
	steps, err := parseInt("steps", 25)
	if err != nil {
		return generationForm{}, errors.New("некорректное число шагов")
	}
	cfg, err := parseFloat("cfg", 7)
	if err != nil {
		return generationForm{}, errors.New("некорректный CFG")
	}
	denoise, err := parseFloat("denoise", 0.7)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила изменения")
	}
	seed, err := parseInt64(r.Form.Get("seed"), -1)
	if err != nil {
		return generationForm{}, errors.New("некорректный seed")
	}
	return generationForm{
		TemplateID: strings.TrimSpace(r.Form.Get("template_id")), Checkpoint: strings.TrimSpace(r.Form.Get("checkpoint")),
		InputImage: strings.TrimSpace(r.Form.Get("input_image")), Positive: strings.TrimSpace(r.Form.Get("positive_prompt")),
		Negative: strings.TrimSpace(r.Form.Get("negative_prompt")), Width: width, Height: height, Steps: steps,
		CFG: cfg, Denoise: denoise, Seed: seed,
	}, nil
}

func parseInt64(value string, fallback int64) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
