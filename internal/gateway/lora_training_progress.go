package gateway

import (
	"regexp"
	"strconv"
	"strings"
)

// Musubi's training progress bar uses the explicit tqdm description "steps".
var trainingStepsLine = regexp.MustCompile(`(?i)^steps:\s*\d{1,3}%\s*\|[^|]*\|\s*(\d+)\s*/\s*(\d+)\s*\[`)

func trainingStepFromLog(lines []string, expected int) (int, bool) {
	if expected <= 0 {
		return 0, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		match := trainingStepsLine.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if len(match) != 3 {
			continue
		}
		step, err1 := strconv.Atoi(match[1])
		total, err2 := strconv.Atoi(match[2])
		if err1 == nil && err2 == nil && total == expected && step >= 0 && step <= total {
			return step, true
		}
	}
	return 0, false
}
