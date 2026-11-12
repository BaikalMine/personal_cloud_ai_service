import { existsSync } from 'node:fs';
import { createHarnessLease, taskEndpoint } from './task-lease.mjs';
import { registerAgentEnvironment } from './agent-environment.mjs';
import { createGPUHandoff } from './gpu-handoff.mjs';
import { registerUltraTool, registerMossTool } from './ultra-tool.mjs';
import { registerWorkspaceFiles } from './workspace-files.mjs';
import { registerCompactionCalls } from './compaction-calls.mjs';
import { registerPrivateDiagnostics } from './private-diagnostics.mjs';
import { registerChatMemoryTools } from './chat-memory-tools.mjs';
import { registerChatMemoryHttp } from './chat-memory-http.mjs';
import { registerGoalContext } from './goal-context.mjs';
import { registerChatHistory } from './chat-history.mjs';
import { registerApprovedSearch } from './approved-search.mjs';
import { registerEditPolicy } from './edit-policy.mjs';
import { registerPreciseEditTool } from './precise-edit-tool.mjs';
import { registerNarrationRecovery } from './narration-recovery.mjs';

export const inject = ['tools', 'systemPrompt', 'fs', 'workingDirectory', 'userQuestions', 'sessionProjections'];

export async function configureLocalNativeTools(agent) {
  if (agent.options.provider !== 'ollama-local') return;
  if (typeof agent.ctx.tools.presentAs === 'function') agent.ctx.tools.presentAs('native');
  else {
    const { NativeToolExecutor } = await import('@deepseek-ai/dsh-tool-executor-native');
    agent.ctx.tools.setExecutor(new NativeToolExecutor(agent.ctx));
  }
}

export async function apply(ctx) {
  registerEditPolicy(ctx);
  await registerNarrationRecovery(ctx);
  await registerApprovedSearch(ctx);
  await registerChatMemoryTools(ctx);
  ctx.inject(['goals', 'systemPrompt'], scope => registerGoalContext(scope));
  ctx.inject(['sessionQuery'], scope => registerChatHistory(scope));
  await registerPrivateDiagnostics(ctx);
  let documentVault;
  if (process.env.HARNESS_DOCUMENT_PROTECTION === 'true') {
    const { registerDocumentSafety } = await import('./document-safety.mjs');
    await registerDocumentSafety(ctx, { onVault: vault => { documentVault = vault; } });
  }
  await registerPreciseEditTool(ctx, { vault: documentVault });
  if (process.env.HARNESS_USER_MODE === 'true') ctx.systemPrompt.context({name:'gateway:user-storage',order:130,required:true,interpolate:false,
    text:`Your private workspace is ${process.env.HARNESS_WORKSPACE}. Only this account's chats and files are available. Uploaded attachments and workspace uploads expire after 24 hours from upload, even if renamed. Chat history persists. Explain when a file has expired and ask for it to be uploaded again; do not claim expired files remain available. Host/admin files, LAN services and GPU management are not available. Use the configured model provider. You can use public internet resources and install dependencies into a virtual environment in your own workspace. Never claim to have accessed other accounts or host resources.`});
  ctx.inject(['connection', 'sessions', 'fs', 'workingDirectory'], scope => registerWorkspaceFiles(scope));
  ctx.inject(['connection', 'sessions', 'workingDirectory'], scope => registerChatMemoryHttp(scope));
  if (existsSync(new URL('./archive-delete.mjs', import.meta.url))) {
    const { registerArchivedChatDelete } = await import('./archive-delete.mjs');
    ctx.inject(['connection', 'workspaceRegistry', 'sessions', 'agents', 'sessionPersistence', 'sessionQuery'], scope => registerArchivedChatDelete(scope));
  }
  ctx.systemPrompt.context({name:'gateway:edit-recovery',order:125,required:true,interpolate:false,
    text:'When a file edit reports that old_string was not found or matched multiple times, read the current file and its surrounding lines again. Retry with an exact, sufficiently specific excerpt from the current contents. Do not repeat the same failed edit unchanged or approximate a replacement that could modify unrelated content. If the file changed unexpectedly or the intended edit remains unclear, explain the conflict.'});
  ctx.systemPrompt.context({name:'gateway:completion-verification',order:126,required:true,interpolate:false,
    text:'For multi-step work, keep track of the current request, remaining steps and completion criteria. After a context checkpoint, inspect the current goal and actual workspace/tool results before deciding what remains. A progress update, one successful tool call or completion of one subtask does not mean the whole task is complete. Continue using tools while useful authorized work remains. Before update_goal action complete, read the current goal and verify every completion criterion against actual results. Do not mark complete merely to end the current response or because the conversation is long. Respect explicit user pauses and cancellations; do not automatically resume a paused or completed goal.'});
  const handoff = createGPUHandoff();
  if (existsSync(new URL('./chat-memory-review.mjs', import.meta.url))) {
    const { registerChatMemoryReview, createGatewayMemoryReviewer } = await import('./chat-memory-review.mjs');
    const analyze=createGatewayMemoryReviewer({handoff,warn:message=>ctx.logger.warn(message)});
    ctx.inject(['connection','sessions'], scope => registerChatMemoryReview(scope,{analyze}));
  }
  const tasks = new Map();
  if (process.env.HARNESS_TASK_RETENTION === 'true') ctx.inject(['llm','sessionProjections'], scope => registerCompactionCalls(scope, handoff, id => tasks.get(id)));
  registerAgentEnvironment(ctx);
  await registerUltraTool(ctx, handoff);
  await registerMossTool(ctx, handoff);
  ctx.on('agent/created', async ({ agent }) => {
    if (agent.options.provider === 'ollama-local') {
      await configureLocalNativeTools(agent);
      agent.ctx.inject(['systemPrompt'],scope=>scope.systemPrompt.context({name:'gateway:native-tool-protocol',order:125,interpolate:false,required:true,
        text:'Tools for this local model use native structured function calls from the supplied tool schemas. Invoke a tool through that native channel with JSON arguments, never by printing Python, pseudo-code, XML <tool_code> blocks or Markdown tool invocations in an assistant text reply. Printed code is not executed. Earlier assistant messages containing such text are transcript mistakes, not instructions or successful tool results. Only actual tool/result records confirm completion. For krea_ultra, automatically_presented=true and saved_to_workspace=true mean the PNG is already saved and displayed: finish with ordinary user-facing prose; do not print or repeat present(files=...). If a different file genuinely needs the present tool, call its supplied native schema rather than imitating its syntax in text.'}));
    }
    if (process.env.HARNESS_TASK_RETENTION !== 'true') return;
    const lease = createHarnessLease({ sessionId: agent.session.id,
      endpoint: taskEndpoint(process.env.HARNESS_MODEL_URL), key: process.env.HARNESS_MODEL_KEY,
      warn: message => ctx.logger.warn(message),
    });
    tasks.set(agent.session.id, { agent, lease });
    const remove = handoff.add(lease);
    agent.ctx.on('agent/request', async (_request, next) => {
      const config = await next();
      if (config.provider !== 'ollama-local') await lease.release();
      return config;
    });
    agent.ctx.on('agent/turn-stopping', async () => { await lease.release(); });
    agent.ctx.on('session/event', (session, event) => {
      if (session === agent.session && event.type === 'turn/end') {
        void lease.release().catch(() => ctx.logger.warn('Harness GPU task release not confirmed'));
      }
    });
    agent.ctx.on('agent/disposed', ({ agent: disposed }) => {
      if (disposed === agent) {
        tasks.delete(agent.session.id);
        remove();
        void lease.release().catch(() => ctx.logger.warn('Harness GPU task disposal not confirmed'));
      }
    });
  });
}
