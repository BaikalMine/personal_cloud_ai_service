package gateway

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"
)

type harnessAgentEvent struct {
	Type string `json:"type"`
	Seq  int64  `json:"seq"`
	Time int64  `json:"time"`
	Data struct {
		Turn   int `json:"turn"`
		Reason struct {
			Kind string `json:"kind"`
		} `json:"reason"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	} `json:"data"`
}

type harnessAgentPage struct {
	Records []json.RawMessage `json:"records"`
	HasMore bool              `json:"hasMore"`
}

func (c *harnessAgentClient) page(r *http.Request, task harnessAgentTask, request map[string]any) (harnessAgentPage, int64, json.RawMessage, error) {
	var projections struct {
		AsOfSeq int64           `json:"asOfSeq"`
		Values  json.RawMessage `json:"values"`
	}
	if err := c.rpc(r.Context(), "session/projections", harnessAgentArgs(map[string]string{"sessionId": task.ID}), &projections); err != nil {
		return harnessAgentPage{}, 0, nil, err
	}
	through := projections.AsOfSeq
	if requested, ok := request["throughSeq"].(int64); ok {
		if requested > through {
			return harnessAgentPage{}, 0, nil, &harnessAgentFault{400, "invalid_through_seq"}
		}
		through = requested
	}
	request["address"] = map[string]string{"kind": "session", "sessionId": task.ID}
	request["throughSeq"] = through
	var page harnessAgentPage
	if err := c.rpc(r.Context(), "session/page", harnessAgentArgs(request), &page); err != nil {
		return page, 0, nil, err
	}
	return page, through, projections.Values, nil
}

func harnessAgentRecord(raw json.RawMessage) (harnessAgentEvent, error) {
	var record struct {
		Event harnessAgentEvent `json:"event"`
	}
	err := json.Unmarshal(raw, &record)
	return record.Event, err
}

func harnessAgentResult(task harnessAgentTask, page harnessAgentPage) map[string]any {
	state := "interrupted"
	if task.Blank {
		state = "created"
	}
	var latestTurn, lastEnd int64 = -1, -1
	var started, ended int64
	reason := ""
	answer := ""
	for _, record := range page.Records {
		event, err := harnessAgentRecord(record)
		if err != nil {
			continue
		}
		switch event.Type {
		case "turn/start":
			latestTurn, started, ended, reason, answer = event.Seq, event.Time, 0, "", ""
		case "assistant/message":
			var text []string
			for _, content := range event.Data.Message.Content {
				if content.Type == "text" && content.Text != "" {
					text = append(text, content.Text)
				}
			}
			if len(text) != 0 {
				answer = strings.Join(text, "\n")
			}
		case "turn/end":
			lastEnd, ended, reason = event.Seq, event.Time, event.Data.Reason.Kind
		}
	}
	if lastEnd > latestTurn {
		switch reason {
		case "completed":
			state = "completed"
		case "aborted":
			state = "cancelled"
		case "max-tokens":
			state = "output_limit"
		case "error":
			state = "failed"
		default:
			state = "stopped"
		}
	}
	if task.Running {
		state = "running"
	}
	duration := int64(0)
	if task.Running && started != 0 {
		ended = time.Now().UnixMilli()
	}
	if started != 0 && ended >= started {
		duration = ended - started
	}
	return map[string]any{"id": task.ID, "state": state, "running": task.Running, "answer": answer,
		"stop_reason": reason, "updated_at": task.UpdatedAt, "elapsed_ms": duration,
		"history_truncated": page.HasMore, "workspace": harnessAgentCwd(task.ID)}
}

func (c *harnessAgentClient) status(w http.ResponseWriter, r *http.Request, task harnessAgentTask) {
	page, through, projections, err := c.page(r, task, map[string]any{"maxMessages": 32})
	if err != nil {
		harnessAgentWriteError(w, err, task.ID)
		return
	}
	result := harnessAgentResult(task, page)
	var control struct {
		Inbox map[string][]json.RawMessage `json:"inbox"`
	}
	if json.Unmarshal(projections, &control) == nil && !task.Running && (len(control.Inbox["next-turn"]) != 0 || len(control.Inbox["next-step"]) != 0) {
		result["state"] = "queued"
	}
	result["through_seq"], result["projections"] = through, projections
	writeJSON(w, 200, result)
}

func (c *harnessAgentClient) events(w http.ResponseWriter, r *http.Request, task harnessAgentTask) {
	request := map[string]any{}
	for _, spec := range []struct {
		query, field string
		fallback     int64
		maximum      int64
	}{{"max_messages", "maxMessages", 32, 100}, {"before_seq", "beforeSeq", -1, math.MaxInt64}, {"through_seq", "throughSeq", -1, math.MaxInt64}} {
		value, err := harnessAgentNumber(r, spec.query, spec.fallback, 0, spec.maximum)
		if err != nil || spec.query == "max_messages" && value < 1 {
			harnessAgentWriteError(w, &harnessAgentFault{400, "invalid_" + spec.query}, task.ID)
			return
		}
		if value >= 0 {
			request[spec.field] = value
		}
	}
	page, through, _, err := c.page(r, task, request)
	if err != nil {
		harnessAgentWriteError(w, err, task.ID)
		return
	}
	var before any
	if page.HasMore && len(page.Records) != 0 {
		event, err := harnessAgentRecord(page.Records[0])
		if err == nil {
			before = event.Seq
		}
	}
	writeJSON(w, 200, map[string]any{"id": task.ID, "through_seq": through, "records": page.Records,
		"has_more": page.HasMore, "next_before_seq": before})
}
