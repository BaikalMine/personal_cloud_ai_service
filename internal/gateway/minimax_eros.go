package gateway

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

var miniMaxErosVersionPattern = regexp.MustCompile(`^h3erosmax[_-]beta([0-9]+)(?:[_.-]|$)`)

func miniMaxErosVersion(name string) int {
	base := path.Base(strings.ToLower(strings.ReplaceAll(name, "\\", "/")))
	match := miniMaxErosVersionPattern.FindStringSubmatch(base)
	if len(match) != 2 {
		return 0
	}
	version, _ := strconv.Atoi(match[1])
	return version
}

func miniMaxErosBeta6(name string) bool { return miniMaxErosVersion(name) == 6 }

func miniMaxErosBeta5Turbo(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
	return strings.Contains(name, "10eros") && strings.Contains(name, "turbo") && strings.Contains(name, "beta5")
}
