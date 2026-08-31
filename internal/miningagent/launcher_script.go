package miningagent

import (
	"fmt"
	"strings"
)

func buildMinerLauncherScript(path, outputLog string) string {
	return fmt.Sprintf(
		`$Host.UI.RawUI.WindowTitle = '%s'; $ErrorActionPreference = 'Continue'; $transcriptStarted = $false; try { try { $null = Start-Transcript -LiteralPath '%s' -Append -Force -ErrorAction Stop; $transcriptStarted = $true } catch { Write-Warning 'Не удалось включить запись лога в файл. Вывод останется в этом окне.' }; Write-Host ([Environment]::NewLine + "=== Запуск майнера $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') ==="); & '%s' } finally { if ($transcriptStarted) { $null = Stop-Transcript } }`,
		powerShellQuote("Майнинг - "+windowsBase(path)),
		powerShellQuote(outputLog),
		powerShellQuote(path),
	)
}

func windowsBase(path string) string {
	normalized := strings.ReplaceAll(path, "/", `\`)
	if index := strings.LastIndex(normalized, `\`); index >= 0 {
		return normalized[index+1:]
	}
	return normalized
}

func powerShellQuote(value string) string {
	return strings.ReplaceAll(value, `'`, `''`)
}
