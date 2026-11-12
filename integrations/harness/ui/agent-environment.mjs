export const AGENT_ENVIRONMENT_CONTEXT = `Managed local-agent environment:
Your shell runs in Linux with Bash, Git, curl, ripgrep, Node.js, Python 3, pip, GCC/G++, make, CMake, Ninja, Java and Ghidra.
/workspace is writable and persistent. Uploaded files are available at their supplied attachment paths. Host Windows paths are not Linux paths.
Python and pip on PATH use /workspace/.venv. Install packages with python3 -m pip install PACKAGE; do not bootstrap pip, run ensurepip, use sudo, or modify system Python. Installed packages persist between tasks.
Public HTTP (port 80) and HTTPS (port 443) are available through HTTP_PROXY/HTTPS_PROXY. curl, Git, npm and pip use these automatically. For custom clients, honor these proxy variables. Direct internet connections and direct public DNS queries from the shell are not available; failure of ping or getent does not mean HTTP access is unavailable. Test with curl -I --max-time 20 https://pypi.org instead.
Internal model requests use the configured provider, not shell requests to host GPUs. Host/LAN/metadata endpoints and non-web ports are unavailable. This does not restrict access to public package repositories or documentation.
Use the available tools to perform and verify the requested work. Do not claim a dependency or network operation succeeded without checking its result. Report genuine failures instead of repeatedly attempting the same unavailable route.`;

export function registerAgentEnvironment(ctx) {
  if (process.env.HARNESS_PYTHON_ENV !== 'true') return;
  ctx.systemPrompt.context({ name: 'gateway:agent-environment', order: 125,
    text: AGENT_ENVIRONMENT_CONTEXT, interpolate: false, required: true });
}
