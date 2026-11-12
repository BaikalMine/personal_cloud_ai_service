package gateway

import "testing"

func TestTrainingStepFromLog(t *testing.T) {
	for _, tc := range []struct {
		name           string
		lines          []string
		expected, step int
		ok             bool
	}{
		{"valid", []string{"steps:  25%|##        | 400/1600 [05:00<15:00, 1.33it/s]"}, 1600, 400, true},
		{"zero", []string{"steps:   0%|          | 0/1600 [00:00<?, ?it/s]"}, 1600, 0, true},
		{"latest", []string{"steps:  25%|##| 400/1600 [00:01<00:02]", "steps:  26%|##| 416/1600 [00:01<00:02]"}, 1600, 416, true},
		{"unrelated", []string{"cache: 100%|###| 1600/1600 [00:01<00:00]", "Loaded 1/1 components"}, 1600, 0, false},
		{"wrong total", []string{"steps: 100%|###| 100/100 [00:01<00:00]"}, 1600, 0, false},
		{"out of bounds", []string{"steps: 100%|###| 1601/1600 [00:01<00:00]"}, 1600, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step, ok := trainingStepFromLog(tc.lines, tc.expected)
			if step != tc.step || ok != tc.ok {
				t.Fatalf("got %d, %v", step, ok)
			}
		})
	}
}
