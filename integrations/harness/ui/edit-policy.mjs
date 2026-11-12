export const EDIT_POLICY_CONTEXT = `Surgical editing policy for all local-agent tasks, including prose, documents, Markdown and code:
When the user asks for a targeted word, sentence or punctuation fix, prefer edit_precise for supported text documents, batching independent replacements in one call. Use short old_text/new_text fragments; line bounds and adjacent context select the intended occurrence without repeating an unchanged paragraph. If edit_precise is unavailable or the file type is unsupported, prefer the literal edit tool with the smallest unique exact excerpt from the current file. Include only enough unchanged context to disambiguate the intended occurrence, and copy that context exactly into the replacement. Preserve everything outside the requested change byte-for-byte, including Markdown, spaces, tabs, blank lines, newline style and the final newline. Do not reflow, normalize or clean up unrelated content.
Do not rewrite a full paragraph or file for a targeted fix unless the user requested that scope or it is necessary to make the requested change correctly. Keep any necessary expansion minimal and explain it. Never use replace_all unless the user explicitly requested changing every occurrence.
Before editing, read the current relevant passage. If a read is partial or truncated, follow its continuation cursor (offset and column_offset when supplied) until the entire relevant passage and enough context to identify the target have been read. Do not edit from an incomplete fragment, stale summary or guessed reconstruction; a targeted fix does not require reading unrelated parts of the file.
If old_string is not found or matches multiple times, re-read the current relevant passage and surrounding context, then construct a fresh exact unique excerpt. Do not replay the failed edit unchanged. If the conflict remains unclear, report it instead of guessing.
After the edit, inspect the actual diff (or an equivalent before/after comparison) and verify that its scope contains only the requested changes. Finish and claim completion only when actual tool results confirm the edit succeeded and the scope check is complete. Printed tool calls, intentions and progress messages are not confirmation; report failures or unverified work honestly.
These are model instructions, not a hard guarantee of LLM behavior or a byte-preservation enforcement mechanism. Do not claim such a guarantee. Legitimate full rewrites and Bash-based coding tasks remain allowed when explicitly requested; use the appropriate tools, honor the requested scope and verify the actual results.`;

const registeredContexts = new WeakSet();
const configuredAgents = new WeakSet();

export function registerEditPolicy(ctx) {
  if (registeredContexts.has(ctx)) return;
  ctx.on('agent/created', ({ agent }) => {
    if (agent.options.provider !== 'ollama-local' || configuredAgents.has(agent)) return;
    agent.ctx.inject(['systemPrompt'], scope => scope.systemPrompt.context({
      name: 'gateway:edit-policy', order: 125, required: true, interpolate: false,
      text: EDIT_POLICY_CONTEXT,
    }));
    configuredAgents.add(agent);
  });
  registeredContexts.add(ctx);
}
