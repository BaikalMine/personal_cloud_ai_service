import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const root=process.argv[2]||'/opt/harness/node_modules';
const file=join(root,'@deepseek-ai/dsh-client-ui-chat/lib/client.js');
let source=await readFile(file,'utf8');
if(!source.includes('gateway.chat.assistant-text')){
  function replace(before,after){if(source.split(before).length!==2)throw new Error('Unsupported Harness chat SDK shape');source=source.replace(before,after);}
  replace('mentions, renderSlot, useGroupAction, t }) {','mentions, renderSlot, useGroupAction, presentedFiles, t }) {');
  replace('rendered.push((0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.MarkdownText, {\n\t\t\t\t\t\t\ttext: block.text,\n\t\t\t\t\t\t\tstreaming,\n\t\t\t\t\t\t\tlabels,\n\t\t\t\t\t\t\tfileMentions: mentions,\n\t\t\t\t\t\t\tpathImages\n\t\t\t\t\t\t}, i));',
    'rendered.push((0, react_jsx_runtime.jsx)(react.Fragment, {children:renderSlot("gateway.chat.assistant-text", {text:block.text,streaming,labels,fileMentions:mentions,pathImages,presentedFiles})}, i));');
  replace('const tail = useTurnData("turn-tail");','const tail = useTurnData("turn-tail");\n\t\t\tconst presentedFiles = useTurnData("deliverables")?.presented || [];');
  replace('\t\t\t\tmentions,\n\t\t\t\tt\n\t\t\t});','\t\t\t\tmentions,\n\t\t\t\tpresentedFiles,\n\t\t\t\tt\n\t\t\t});');
  const oldSlot = 'children: { "conversation.chat.reasoning.body": {';
  const newSlot = 'children: {\n\t\t\t\t\t"conversation.chat.reasoning.body": {';
  if (source.includes(oldSlot)) replace(oldSlot, 'children: { "gateway.chat.assistant-text": {kind:"single",scope:"session"}, "conversation.chat.reasoning.body": {');
  else replace(newSlot, 'children: {\n\t\t\t\t\t"gateway.chat.assistant-text": {kind:"single",scope:"session"},\n\t\t\t\t\t"conversation.chat.reasoning.body": {');
  const start=source.indexOf('const ReasoningRow ='),end=source.indexOf('\n\t\t//#endregion',start);
  if(start<0||end<0)throw new Error('Unsupported Harness reasoning SDK shape');
  let row=source.slice(start,end);
  const before='const { expanded, toggle } = useDisclosure();';
  if(row.split(before).length!==2||row.split('onToggle: toggle,').length!==2)throw new Error('Unsupported Harness reasoning disclosure shape');
  row=row.replace(before,'const { expanded, toggle, setExpanded } = useDisclosure();\n\t\t\tconst gatewayReasoningTouched = (0, react.useRef)(false);\n\t\t\tconst gatewayLiveReasoning = usePresentation(policy => !policy.foldCompletedTurns);\n\t\t\t(0, react.useEffect)(() => { if (running && gatewayLiveReasoning && !gatewayReasoningTouched.current) setExpanded(true); }, [running, gatewayLiveReasoning, setExpanded]);');
  row=row.replace('onToggle: toggle,','onToggle: () => { gatewayReasoningTouched.current = true; toggle(); },');
  source=source.slice(0,start)+row+source.slice(end);
  await writeFile(file,source);
}
if(!source.includes('gatewayLiveReasoning'))throw new Error('Incomplete Harness chat patch');
