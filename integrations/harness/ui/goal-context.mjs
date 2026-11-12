const PHASES = new Set(['active', 'paused', 'blocked', 'complete']);
const ACTIVATIONS = new Set(['armed', 'disarmed']);

function snapshot(goals, agent) {
  if (!agent) return 'unknown';
  try {
    const goal = goals.get(agent);
    if (goal === undefined) return 'none';
    if (!goal || !PHASES.has(goal.phase) || !Number.isSafeInteger(goal.revision) || goal.revision < 1 || !ACTIVATIONS.has(goal.activation)) return 'unknown';
    return `phase=${goal.phase}; revision=${goal.revision}; activation=${goal.activation}`;
  } catch {
    return 'unknown';
  }
}

// Register with goals and systemPrompt injected; assembly supplies the exact
// agent, including subagents, and refreshContext evaluates it again per step.
export function registerGoalContext(ctx) {
  ctx.systemPrompt.context({
    name:'gateway:goal-state', order:126, required:true, interpolate:false,
    text:({ agent } = {}) => `Current session goal state: ${snapshot(ctx.goals, agent)}. This state supersedes older goal-round instructions and summaries. A completed, paused, blocked, absent, or unknown goal is not an instruction to continue prior work. If the state is unknown, verify it before continuing an old goal. An active but disarmed goal does not authorize automatic continuation. Address a new direct user request normally; a new goal may be created when supported by that request, not by an old summary alone. For an active goal, verify its current objective using get_goal and follow the latest user instructions.`,
  });
}
