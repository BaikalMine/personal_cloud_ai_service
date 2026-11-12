import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';

const modules = process.argv[2] ?? '/opt/harness/node_modules';

function patch(pkg, filename, edits) {
  const path = join(modules, '@deepseek-ai', pkg, 'lib', filename);
  let source = readFileSync(path, 'utf8').replace(/\r\n/g, '\n');
  for (const [before, after] of edits) {
    if (source.split(before).length !== 2) throw new Error(`Unexpected ${pkg} patch anchor: ${before.trim()}`);
    source = source.replace(before, () => after);
  }
  writeFileSync(path, source);
  execFileSync(process.execPath, ['--check', path], { stdio:'inherit' });
}

const blocked = id => `globalThis[Symbol.for("ai-gateway.archiveDeleting")]?.has(${id})`;
const guard = id => `if (${blocked(id)} || archiveDeleteStaged(${id})) throw new Error("Archived chat deletion is in progress");`;
const staged = `import { existsSync as archiveDeleteExists } from "node:fs";
import { join as archiveDeleteJoin } from "node:path";
const archiveDeleteStaged = id => typeof id === "string" && /^[a-zA-Z0-9_-]{1,128}$/.test(id) &&
  process.env.DSH_HOME && archiveDeleteExists(archiveDeleteJoin(process.env.DSH_HOME, "archive-delete", id));
`;

patch('dsh-agent-loop', 'index.js', [
  ['ready = true;\n\t\t\t\t\treturn {', 'Object.defineProperty(agent, Symbol.for("ai-gateway.archiveDispose"), { value: dispose });\n\t\t\t\t\tready = true;\n\t\t\t\t\treturn {'],
]);
patch('dsh-session-persistence-jsonl', 'index.js', [
  ['import { readdirSync } from "node:fs";', 'import { readdirSync } from "node:fs";\n' + staged],
  ['this.tracker.registerCreated(snapshot, inheritedEventCount);', guard('snapshot.id') + '\n\t\tthis.tracker.registerCreated(snapshot, inheritedEventCount);'],
  ['this.tracker.claimWrite(id);', guard('id') + '\n\t\tthis.tracker.claimWrite(id);'],
]);
patch('dsh-session-projection-cache', 'index.js', [
  ['async put(id, identity, rows) {', `async put(id, identity, rows) {\n\t\tif (${blocked('id')}) return;`],
]);
patch('dsh-workspace', 'index.js', [
  ['unarchiveSession(sessionId) {\n\t\treturn this.enqueueOperation(async () => {', 'unarchiveSession(sessionId) {\n\t\treturn this.enqueueOperation(async () => {\n\t\t\t' + guard('sessionId')],
  ['var WorkspaceRegistry = class', staged + 'var WorkspaceRegistry = class'],
]);

const menu = `
      const deletingChats = new Set();
      register("permanent-delete", 650, ["workspace/unarchiveSession"], (target) => {
        if (!archived(target) || !allowed(target, "workspace/unarchiveSession")) return [];
        const archiveRussian = ctx.locale.getSnapshot().active === "ru";
        const label = archiveRussian ? "Удалить навсегда" : "Delete permanently";
        const title = target.displayTitle || target.sessionId;
        return [{
          id: "permanent-delete",
          label,
          danger: true,
          afterContent: true,
          icon: { key: "workspace.delete" },
          async run() {
            const id = target.sessionId;
            if (deletingChats.has(id) || !archived(target)) return;
            if (!window.confirm(archiveRussian
              ? \`Удалить чат «\${title}» навсегда? История и постоянная память чата будут удалены. Файлы рабочей папки и память Mnemon сохранятся. Отменить удаление нельзя.\`
              : \`Permanently delete “\${title}”? Chat history and permanent chat memory will be removed. Workspace files and Mnemon memory will be kept. This cannot be undone.\`)) return;
            deletingChats.add(id);
            try {
              const response = await fetch(new URL("./api/gateway-archive/delete", document.baseURI), {
                method: "POST", credentials: "same-origin", mode: "same-origin", redirect: "error",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ sessionId: id, confirm: true })
              });
              const result = await response.json();
              if (!response.ok || result.ok !== true) {
                const messages = {
                  chat_busy: archiveRussian ? "Чат ещё занят. Дождитесь завершения работы и повторите." : "This chat is still busy. Wait until it stops and try again.",
                  not_archived: archiveRussian ? "Чат больше не находится в архиве." : "This chat is no longer in the archive."
                };
                window.alert(messages[result.error?.code] ?? (archiveRussian ? "Не удалось завершить удаление. Попробуйте ещё раз." : "Could not finish deleting the chat. Try again."));
              }
            } catch {
              window.alert(archiveRussian ? "Не удалось подтвердить удаление. Проверьте архив." : "Could not confirm deletion. Check the archive.");
            } finally { deletingChats.delete(id); }
          }
        }];
      });
`;
const end = '\n\t\t}\n\t\t//#endregion\n\t\t//#region lib/types/client/session-actions/PinSession.js';
patch('dsh-client-ui-workspace', 'client.js', [[end, '\n' + menu + end]]);
