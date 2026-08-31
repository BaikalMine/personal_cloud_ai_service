package miningagent

import (
	"strings"
	"testing"
)

func TestMinerLauncherKeepsNativeOutputAttachedToConsole(t *testing.T) {
	script := buildMinerLauncherScript(
		`C:\Mining\O'Brien\start-mining.bat`,
		`C:\ProgramData\AI-Mining-Agent\miner.log`,
	)

	for _, required := range []string{
		`$Host.UI.RawUI.WindowTitle = 'Майнинг - start-mining.bat'`,
		`Start-Transcript -LiteralPath 'C:\ProgramData\AI-Mining-Agent\miner.log' -Append`,
		`& 'C:\Mining\O''Brien\start-mining.bat'`,
		`Stop-Transcript`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("launcher script is missing %q: %s", required, script)
		}
	}
	for _, bufferedPipeline := range []string{"Tee-Object", "2>&1 |"} {
		if strings.Contains(script, bufferedPipeline) {
			t.Fatalf("launcher script still buffers native output through %q", bufferedPipeline)
		}
	}
}

func TestWindowsBaseHandlesBothSeparators(t *testing.T) {
	for _, path := range []string{`C:\Mining\start.bat`, `C:/Mining/start.bat`} {
		if got := windowsBase(path); got != "start.bat" {
			t.Fatalf("windowsBase(%q) = %q", path, got)
		}
	}
}
