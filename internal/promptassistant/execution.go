package promptassistant

// ExecutionOutcome describes executor evidence, independently of output quality.
// HTTP errors, cancellation and malformed responses do not prove that inference
// stopped. Only a complete Ollama response with done=true confirms completion.
type ExecutionOutcome uint8

const (
	ExecutionNotDispatched ExecutionOutcome = iota
	ExecutionUnconfirmed
	ExecutionCompleted
)

func (outcome ExecutionOutcome) Settled() bool {
	return outcome == ExecutionNotDispatched || outcome == ExecutionCompleted
}
