package modelruntime

import "time"

type Config struct {
	Executable      string
	ModelsDirectory string
	StateDirectory  string
	Address         string
	StartupTimeout  time.Duration
	StopTimeout     time.Duration
	ContextLength   int
	FlashAttention  string
	KVCacheType     string
}

type Result struct {
	Status     int
	Body       []byte
	Dispatched bool
	Stopped    bool
	Retained   bool
}
