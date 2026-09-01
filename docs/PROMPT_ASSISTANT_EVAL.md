# Fixed prompt-assistant evaluation

`cmd/prompt-assistant-eval` compares local prompt-assistant models on one versioned suite instead of on hand-picked successful prompts. It calls Ollama directly and does not start ComfyUI or generate user media.

## Fixed suite

Suite `prompt-assistant-fixed-v1` contains seven non-sensitive cases:

| Case | Family | What it checks |
|---|---|---|
| `krea2-appearance` | Krea2 text-to-image | appearance details |
| `krea2-clothing` | Krea2 image edit | identity and clothing transfer |
| `flux2-object` | Flux2 edit | object transfer |
| `flux2-style-background` | Flux2 edit | style and background roles |
| `minimax-first-last-frame` | MiniMax H3 FL2VA | exact first and last frames |
| `minimax-sound` | MiniMax H3 Ref2VA | `Audio 1`, voice and synchronization |
| `minimax-motion` | MiniMax H3 Ref2VA | `Picture 1`, `Video 1`, motion and timing |

Image references are deterministic 512 px PNG cards generated in memory. They contain distinct visual layouts and short labels. This keeps the comparison reproducible and exercises vision input plus reference numbering; it is not an image-generation quality benchmark.

Each case records the final prompt, structured reference map, latency, token counts, request policy, missing concepts and errors. The score is composed of:

- 25 points for a valid prompt;
- 35 points for the exact ordered reference map;
- 40 points for preserving the required case concepts.

The report includes a suite fingerprint. Reports with different fingerprints cannot be compared.

## Run a baseline

From PowerShell, using the repository's pinned Go image:

```powershell
docker run --rm -v "${PWD}:/src" -w /src `
  golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 `
  go run ./cmd/prompt-assistant-eval `
  -ollama-url http://host.docker.internal:11434 `
  -model huihui_ai/gemma-4-abliterated:e4b `
  -label e4b-production `
  -output artifacts/prompt-assistant-eval/e4b-production.json
```

The seven requests run sequentially so the suite does not create a VRAM burst.

## Compare a candidate

```powershell
docker run --rm -v "${PWD}:/src" -w /src `
  golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 `
  go run ./cmd/prompt-assistant-eval `
  -ollama-url http://host.docker.internal:11434 `
  -model candidate-model:latest `
  -label candidate `
  -baseline artifacts/prompt-assistant-eval/e4b-production.json `
  -output artifacts/prompt-assistant-eval/candidate.json
```

Add `-fail-on-regression` in CI to return a non-zero exit code when the candidate loses passed cases or mean score. `-think`, token budgets, timeouts and `keep-alive` are explicit flags, so a policy change can be measured separately from a model change.

Reports under `artifacts/` are local test output and are not committed by default.
