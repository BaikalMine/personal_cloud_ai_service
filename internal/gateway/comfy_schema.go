package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultComfyObjectInfoCacheTTL = 30 * time.Second
	defaultComfyObjectInfoMaxStale = 24 * time.Hour
)

type comfyObjectInfoSource string

const (
	comfyObjectInfoLive          comfyObjectInfoSource = "live"
	comfyObjectInfoFreshCache    comfyObjectInfoSource = "fresh_cache"
	comfyObjectInfoLastKnownGood comfyObjectInfoSource = "last_known_good"
)

type comfyObjectInfoSnapshot struct {
	Info          map[string]comfyNodeInfo
	Schema        comfySchemaCatalog
	Source        comfyObjectInfoSource
	FetchedAt     time.Time
	LastAttemptAt time.Time
	Fingerprint   string
	LastError     string
}

func (snapshot comfyObjectInfoSnapshot) sourceLabel() string {
	switch snapshot.Source {
	case comfyObjectInfoLive:
		return "получен из ComfyUI"
	case comfyObjectInfoFreshCache:
		return "свежий кэш Gateway"
	case comfyObjectInfoLastKnownGood:
		return "последний рабочий каталог"
	default:
		return "источник неизвестен"
	}
}

type comfySchemaCatalog struct {
	Nodes       map[string]comfyNodeSchema
	FetchedAt   time.Time
	Fingerprint string
}

type comfyNodeSchema struct {
	ClassType         string
	DisplayName       string
	Category          string
	PythonModule      string
	SchemaFingerprint string
	Required          map[string]comfyInputSchema
	Optional          map[string]comfyInputSchema
	Hidden            map[string]comfyInputSchema
	Outputs           []string
}

type comfyInputSchema struct {
	Name           string
	Type           string
	Choices        []any
	Default        any
	Min            *float64
	Max            *float64
	DynamicOptions map[string]comfyDynamicOption
}

type comfyDynamicOption struct {
	Key      string
	Required map[string]comfyInputSchema
	Optional map[string]comfyInputSchema
}

type comfyObjectInfoCache struct {
	mu          sync.Mutex
	now         func() time.Time
	cacheTTL    time.Duration
	maxStale    time.Duration
	nextRefresh time.Time
	snapshot    comfyObjectInfoSnapshot
	hasSnapshot bool
	lastError   string
	lastAttempt time.Time
}

func newComfyObjectInfoCache(cacheTTL, maxStale time.Duration, now func() time.Time) *comfyObjectInfoCache {
	if cacheTTL <= 0 {
		cacheTTL = defaultComfyObjectInfoCacheTTL
	}
	if maxStale < cacheTTL {
		maxStale = defaultComfyObjectInfoMaxStale
		if maxStale < cacheTTL {
			maxStale = cacheTTL
		}
	}
	if now == nil {
		now = time.Now
	}
	return &comfyObjectInfoCache{now: now, cacheTTL: cacheTTL, maxStale: maxStale}
}

func (a *App) comfySchemaCache() *comfyObjectInfoCache {
	a.objectInfoOnce.Do(func() {
		a.objectInfoCache = newComfyObjectInfoCache(a.cfg.ComfyObjectInfoCacheTTL, a.cfg.ComfyObjectInfoMaxStale, time.Now)
	})
	return a.objectInfoCache
}

func (a *App) comfyObjectInfo(ctx context.Context, force bool) (comfyObjectInfoSnapshot, error) {
	cache := a.comfySchemaCache()
	cache.mu.Lock()
	defer cache.mu.Unlock()

	now := cache.now().UTC()
	if cache.hasSnapshot && !force && now.Before(cache.nextRefresh) {
		result := cache.snapshot
		result.Source = comfyObjectInfoFreshCache
		result.LastAttemptAt = cache.lastAttempt
		result.LastError = cache.lastError
		return result, nil
	}

	cache.lastAttempt = now
	snapshot, err := a.fetchComfyObjectInfo(ctx, now)
	if err == nil {
		cache.snapshot = snapshot
		cache.hasSnapshot = true
		cache.lastError = ""
		cache.nextRefresh = now.Add(cache.cacheTTL)
		snapshot.Source = comfyObjectInfoLive
		snapshot.LastAttemptAt = now
		return snapshot, nil
	}

	cache.lastError = err.Error()
	cache.nextRefresh = now.Add(minDuration(cache.cacheTTL, 10*time.Second))
	if cache.hasSnapshot && now.Sub(cache.snapshot.FetchedAt) <= cache.maxStale {
		result := cache.snapshot
		result.Source = comfyObjectInfoLastKnownGood
		result.LastAttemptAt = now
		result.LastError = cache.lastError
		return result, nil
	}
	return comfyObjectInfoSnapshot{Source: comfyObjectInfoLastKnownGood, LastAttemptAt: now, LastError: cache.lastError}, err
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func (a *App) fetchComfyObjectInfo(ctx context.Context, fetchedAt time.Time) (comfyObjectInfoSnapshot, error) {
	if a.cfg.ComfyUIUpstream == nil {
		return comfyObjectInfoSnapshot{}, errors.New("ComfyUI не настроен")
	}
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/object_info")
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return comfyObjectInfoSnapshot{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ai-access-gateway/workflow-compatibility")
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return comfyObjectInfoSnapshot{}, fmt.Errorf("каталог нод ComfyUI недоступен: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
		return comfyObjectInfoSnapshot{}, fmt.Errorf("каталог нод ComfyUI вернул HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxComfyObjectInfo+1))
	if err != nil {
		return comfyObjectInfoSnapshot{}, fmt.Errorf("не удалось прочитать каталог нод ComfyUI: %w", err)
	}
	if len(body) > maxComfyObjectInfo {
		return comfyObjectInfoSnapshot{}, errors.New("каталог нод ComfyUI превышает допустимый размер")
	}
	var info map[string]comfyNodeInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return comfyObjectInfoSnapshot{}, fmt.Errorf("некорректный каталог нод ComfyUI: %w", err)
	}
	if len(info) == 0 {
		return comfyObjectInfoSnapshot{}, errors.New("ComfyUI вернул пустой каталог нод")
	}
	fingerprint := hashBytes(body)
	return comfyObjectInfoSnapshot{
		Info: info, Schema: buildComfySchemaCatalog(info, fetchedAt, fingerprint), Source: comfyObjectInfoLive,
		FetchedAt: fetchedAt, LastAttemptAt: fetchedAt, Fingerprint: fingerprint,
	}, nil
}

func buildComfySchemaCatalog(info map[string]comfyNodeInfo, fetchedAt time.Time, fingerprint string) comfySchemaCatalog {
	nodes := make(map[string]comfyNodeSchema, len(info))
	for classType, nodeInfo := range info {
		node := comfyNodeSchema{
			ClassType: classType, DisplayName: strings.TrimSpace(nodeInfo.DisplayName), Category: strings.TrimSpace(nodeInfo.Category),
			PythonModule: strings.TrimSpace(nodeInfo.PythonModule), Required: parseComfyInputMap(nodeInfo.Input.Required),
			Optional: parseComfyInputMap(nodeInfo.Input.Optional), Hidden: parseComfyInputMap(nodeInfo.Input.Hidden),
			Outputs: parseComfyOutputTypes(nodeInfo.Output),
		}
		if node.DisplayName == "" {
			node.DisplayName = classType
		}
		if encoded, err := json.Marshal(nodeInfo); err == nil {
			node.SchemaFingerprint = hashBytes(encoded)
		}
		nodes[classType] = node
	}
	return comfySchemaCatalog{Nodes: nodes, FetchedAt: fetchedAt, Fingerprint: fingerprint}
}

func parseComfyOutputTypes(raw []json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, value := range raw {
		var decoded any
		if decodeJSON(value, &decoded) != nil {
			result = append(result, "*")
			continue
		}
		types := flattenComfyTypes(decoded, nil)
		if len(types) == 0 {
			result = append(result, "*")
			continue
		}
		result = append(result, strings.Join(types, ","))
	}
	return result
}

func flattenComfyTypes(value any, result []string) []string {
	switch typed := value.(type) {
	case string:
		if value := strings.TrimSpace(typed); value != "" {
			result = append(result, value)
		}
	case []any:
		for _, item := range typed {
			result = flattenComfyTypes(item, result)
		}
	}
	return result
}

func parseComfyInputMap(raw map[string]json.RawMessage) map[string]comfyInputSchema {
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string]comfyInputSchema, len(raw))
	for name, definition := range raw {
		if schema, ok := parseComfyInputSchema(name, definition); ok {
			result[name] = schema
		}
	}
	return result
}

func parseComfyInputSchema(name string, raw json.RawMessage) (comfyInputSchema, bool) {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return comfyInputSchema{}, false
	}
	schema := comfyInputSchema{Name: name}
	if err := json.Unmarshal(parts[0], &schema.Type); err != nil {
		var choices []any
		if decodeJSON(parts[0], &choices) != nil {
			return comfyInputSchema{}, false
		}
		schema.Type = "COMBO"
		schema.Choices = choices
	}
	if len(parts) < 2 {
		return schema, true
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(parts[1], &options); err != nil {
		return schema, true
	}
	if rawDefault, ok := options["default"]; ok {
		_ = decodeJSON(rawDefault, &schema.Default)
	}
	schema.Min = rawNumberPointer(options["min"])
	schema.Max = rawNumberPointer(options["max"])
	if schema.Type == "COMBO" && len(schema.Choices) == 0 {
		_ = decodeJSON(options["options"], &schema.Choices)
	}
	if schema.Type == "COMFY_DYNAMICCOMBO_V3" {
		var dynamicOptions []struct {
			Key    string `json:"key"`
			Inputs struct {
				Required map[string]json.RawMessage `json:"required"`
				Optional map[string]json.RawMessage `json:"optional"`
			} `json:"inputs"`
		}
		if json.Unmarshal(options["options"], &dynamicOptions) == nil {
			schema.DynamicOptions = make(map[string]comfyDynamicOption, len(dynamicOptions))
			for _, option := range dynamicOptions {
				key := strings.TrimSpace(option.Key)
				if key == "" {
					continue
				}
				schema.Choices = append(schema.Choices, key)
				schema.DynamicOptions[key] = comfyDynamicOption{Key: key, Required: parseComfyInputMap(option.Inputs.Required), Optional: parseComfyInputMap(option.Inputs.Optional)}
			}
		}
	}
	return schema, true
}

func rawNumberPointer(raw json.RawMessage) *float64 {
	if len(raw) == 0 {
		return nil
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func decodeJSON(raw json.RawMessage, destination any) error {
	if len(raw) == 0 {
		return errors.New("empty JSON value")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	return decoder.Decode(destination)
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func shortFingerprint(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
