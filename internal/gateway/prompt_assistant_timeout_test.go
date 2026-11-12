package gateway

import (
	"ai-access-gateway/internal/promptassistant"
	"testing"
	"time"
)

func TestAssistantDispatchReasoningDeadline(t *testing.T) {
	a := &App{}
	var snapshot promptAssistantJobSnapshot
	snapshot.Request.Policy = promptassistant.RequestPolicy{TimeoutMS: 300000}
	snapshot.Think = true
	if got := a.assistantDispatchTimeout(snapshot); got != 15*time.Minute {
		t.Fatal(got)
	}
	if snapshot.Request.Policy.TimeoutMS != 300000 {
		t.Fatal("accepted metadata modified")
	}
	snapshot.Think = false
	if got := a.assistantDispatchTimeout(snapshot); got != 5*time.Minute {
		t.Fatal(got)
	}
	snapshot.Think = true
	snapshot.ModelID = "admin-model"
	if got := a.assistantDispatchTimeout(snapshot); got != 5*time.Minute {
		t.Fatal(got)
	}
}
