import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { generatedImageTest, upgradeGeneratedImageMatcher } from './generated-image-path.mjs';

async function bundle(root, name) {
  const dir = join(root, '@deepseek-ai', name);
  const manifest = JSON.parse(await readFile(join(dir, 'package.json'), 'utf8'));
  if (manifest.version !== '0.2.2-rc.1') throw new Error(`Unsupported ${name} version`);
  const path = join(dir, 'lib/client.js');
  return { path, source: await readFile(path, 'utf8') };
}
function replace(source, before, after) {
  if (source.split(before).length !== 2) throw new Error('Unexpected rc.1 UI anchor');
  return source.replace(before, after);
}

export async function patchChat022(root) {
  const { path, source: original } = await bundle(root, 'dsh-client-ui-chat');
  if (original.includes('gateway.chat.assistant-text')) {
    if (!original.includes('gatewayLiveReasoning') || !original.includes('pathImages,textInteractionSource,presentedFiles')) throw new Error('Incomplete rc.1 chat patch');
    return;
  }
  let source = replace(original, 'mentions, renderSlot, useGroupAction, t }) {', 'mentions, renderSlot, useGroupAction, presentedFiles, t }) {');
  source = replace(source, `rendered.push((0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.MarkdownText, {
\t\t\t\t\t\t\ttext: block.text,
\t\t\t\t\t\t\tstreaming,
\t\t\t\t\t\t\tlabels,
\t\t\t\t\t\t\tfileMentions: mentions,
\t\t\t\t\t\t\tpathImages,
\t\t\t\t\t\t\ttextInteractionSource
\t\t\t\t\t\t}, i));`, 'rendered.push((0, react_jsx_runtime.jsx)(react.Fragment, {children:renderSlot("gateway.chat.assistant-text", {text:block.text,streaming,labels,fileMentions:mentions,pathImages,textInteractionSource,presentedFiles})}, i));');
  source = replace(source, 'const tail = useTurnData("turn-tail");', 'const tail = useTurnData("turn-tail");\n\t\t\tconst presentedFiles = useTurnData("deliverables")?.presented || [];');
  source = replace(source, '\t\t\t\tmentions,\n\t\t\t\tt\n\t\t\t});', '\t\t\t\tmentions,\n\t\t\t\tpresentedFiles,\n\t\t\t\tt\n\t\t\t});');
  source = replace(source, 'children: {\n\t\t\t\t\t"conversation.chat.reasoning.body": {', 'children: {\n\t\t\t\t\t"gateway.chat.assistant-text": {kind:"single",scope:"session"},\n\t\t\t\t\t"conversation.chat.reasoning.body": {');
  const start = source.indexOf('const ReasoningRow ='), end = source.indexOf('\n\t\t//#endregion', start);
  if (start < 0 || end < 0) throw new Error('Missing rc.1 reasoning row');
  let row = source.slice(start, end);
  row = replace(row, 'const { expanded, toggle } = useDisclosure();', 'const { expanded, toggle, setExpanded } = useDisclosure();\n\t\t\tconst gatewayReasoningTouched = (0, react.useRef)(false);\n\t\t\tconst gatewayLiveReasoning = usePresentation(policy => !policy.foldCompletedTurns);\n\t\t\t(0, react.useEffect)(() => { if (running && gatewayLiveReasoning && !gatewayReasoningTouched.current) setExpanded(true); }, [running, gatewayLiveReasoning, setExpanded]);');
  row = replace(row, 'onToggle: toggle,', 'onToggle: () => { gatewayReasoningTouched.current = true; toggle(); },');
  await writeFile(path, source.slice(0, start) + row + source.slice(end));
}

export async function patchUltra022(root) {
  const { path, source: original } = await bundle(root, 'dsh-client-ui-deliverables');
  if (original.includes('gateway.deliverables.image')) {
    if (!original.includes('if (generatedImage) return generatedImage;') || !original.includes('krea-ultra|moss-image')) throw new Error('Incomplete rc.1 deliverables patch');
    const upgraded = upgradeGeneratedImageMatcher(original);
    if (upgraded !== original) await writeFile(path, upgraded);
    return;
  }
  let source = replace(original, 'function PresentedFileCard({ file, cwd, phase, host, onPreview, contextMenuProps, actions, t }) {', 'function PresentedFileCard({ file, cwd, phase, host, onPreview, contextMenuProps, actions, t, generatedImage }) {\n\t\t\tif (generatedImage) return generatedImage;');
  source = replace(source, 'children: presented.map((file) => (0, react_jsx_runtime.jsx)(PresentedFileCard, {\n\t\t\t\t\t\t\tfile,', 'children: presented.map((file) => (0, react_jsx_runtime.jsx)(PresentedFileCard, {\n\t\t\t\t\t\t\tfile,\n\t\t\t\t\t\t\tgeneratedImage: '+generatedImageTest+' ? renderSlot("gateway.deliverables.image", {file, sessionId}) : null,');
  source = replace(source, 'children: { "deliverables.file.actions": {', 'children: { "gateway.deliverables.image": {kind:"single",scope:"session"}, "deliverables.file.actions": {');
  await writeFile(path, source);
}

export async function patchFiles022(root) {
  const { path, source: original } = await bundle(root, 'dsh-client-ui-sidebar-files');
  if (original.includes('gateway.files.row.actions')) {
    if (!original.includes('renderGatewayActions') || !original.includes('input.renderGatewayActions?.(entry.path, kind)')) throw new Error('Incomplete rc.1 files patch');
    return;
  }
  let source = replace(original, 'function FileNavigator({ useTabInfo, page, onOpen, sessionId, useSessions, useStore, actions, useRowWidth, useCanOpen, attach, placeBeside, hold, toggle, scrolled, bodyDrawn, currentDrawn, navigatorDrawn, t, renderFactorySlot, useContextMenu }) {', 'function FileNavigator({ useTabInfo, page, onOpen, sessionId, useSessions, useStore, actions, useRowWidth, useCanOpen, attach, placeBeside, hold, toggle, scrolled, bodyDrawn, currentDrawn, navigatorDrawn, t, renderFactorySlot, renderSlot, useContextMenu }) {');
  source = replace(source, 'current: location?.current,\n\t\t\t\t\t\tcurrentRef', 'current: location?.current,\n\t\t\t\t\t\trenderGatewayActions: (absolutePath, entryType) => renderSlot("gateway.files.row.actions", { absolutePath, entryType }),\n\t\t\t\t\t\tcurrentRef');
  const accessory = 'children: (0, react_jsx_runtime.jsx)(Accessory, { entry })';
  if (source.split(accessory).length !== 4) throw new Error('Unexpected rc.1 tree accessory anchors');
  source = source.replaceAll(accessory, 'children: (0, react_jsx_runtime.jsxs)(react.Fragment, { children: [(0, react_jsx_runtime.jsx)(Accessory, { entry }), (kind === "file" || kind === "directory") && input.renderGatewayActions?.(entry.path, kind)] })');
  source = replace(source, 'name: "sidebar.files.navigator",\n\t\t\t\tscope: "session",\n\t\t\t\tlocale: NS,\n\t\t\t\tstore,', 'name: "sidebar.files.navigator",\n\t\t\t\tscope: "session",\n\t\t\t\tlocale: NS,\n\t\t\t\tchildren: { "gateway.files.row.actions": { kind: "list", scope: "session" } },\n\t\t\t\tstore,');
  await writeFile(path, source);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [kind, root = '/opt/harness/node_modules'] = process.argv.slice(2);
  if (kind === 'chat') await patchChat022(root);
  else if (kind === 'ultra') await patchUltra022(root);
  else if (kind === 'files') await patchFiles022(root);
  else throw new Error('Choose chat, ultra or files');
}
