export function registerCompactionCalls(ctx, handoff, taskFor) {
  // All local streams share one queue, including delegated agents. Acquiring
  // in agent/request is too early: compaction may run before that reply streams.
  ctx.on('llm/stream', (options, next) => {
    if (options.provider !== 'ollama-local') return next();
    return (async function* () {
      const task = taskFor(options.sessionId);
      if (!task) throw new Error('Harness model call requires a registered local agent task');
      const compact = options.purpose === 'compaction';
      const signal = options.signal;
      const done = await handoff.model(task.lease, signal);
      try {
        if (compact) {
          const info = await ctx.llm.resolveModelInfo(options.provider, options.model, signal);
          if (!Object.isFrozen(options) && info.reasoning?.efforts?.some(effort => effort.id === 'none')) options.reasoningEffort = 'none';
        }
        yield* next();
      } finally {
        try {
          const boundary = ctx.sessionProjections.stateOf(task.agent.session, 'turnBoundary');
          if (!boundary || boundary.openTurnStartSeq === null) await task.lease.release();
        } finally { done(); }
      }
    })();
  });
}
