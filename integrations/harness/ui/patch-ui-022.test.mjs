import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import vm from 'node:vm';
import { patchChat022, patchUltra022, patchFiles022 } from './patch-ui-022.mjs';

for (const [name, patch] of [['dsh-client-ui-chat', patchChat022], ['dsh-client-ui-deliverables', patchUltra022], ['dsh-client-ui-sidebar-files', patchFiles022]]) {
  test(`rc.1 ${name} patch preserves native anchors and fails closed`, async t => {
    const installed = process.env.HARNESS_022_DEEPSEEK_ROOT;
    if (!installed) return t.skip('requires rc.1 package tree');
    const root = await mkdtemp(join(tmpdir(), 'harness-ui-022-'));
    t.after(() => rm(root, { recursive:true, force:true }));
    const folder = join(root, '@deepseek-ai', name), file = join(folder, 'lib/client.js');
    await mkdir(join(folder, 'lib'), { recursive:true });
    await writeFile(join(folder, 'package.json'), await readFile(join(installed, name, 'package.json')));
    const before = await readFile(join(installed, name, 'lib/client.js'), 'utf8');
    await writeFile(file, before);
    await patch(root);
    const after = await readFile(file, 'utf8');
    new vm.Script(after);
    if (name.endsWith('chat')) {
      assert.match(after, /pathImages,textInteractionSource,presentedFiles/);
      assert.match(after, /gatewayReasoningTouched\.current = true; toggle\(\)/);
      assert.match(after, /useTurnData\("deliverables"\)/);
      const start = after.indexOf('const AssistantMarkdown ='), end = after.indexOf('\n\t\tfunction ProcessReasoning', start);
      const jsx = (type, props) => ({type, props}), calls = [];
      const context = {react:{memo:x=>x,useMemo:fn=>fn(),Fragment:'fragment'}, react_jsx_runtime:{jsx,jsxs:jsx},
        markdownLabels:()=>({}), document:{baseURI:'http://fixture/'}, AssistantMarkdown_module_css_default:{root:'root',body:'body',stopped:'stopped'}};
      const Component = vm.runInNewContext(`${after.slice(start,end)}; AssistantMarkdown`, context);
      const files=[{path:'synthetic.png'}], mentions={};
      Component({blocks:[{kind:'text',text:'synthetic'}],streaming:false,textInteractionSource:true,presentedFiles:files,mentions,
        renderSlot:(name,props)=>{calls.push({name,props});return null;},t:()=>''});
      assert.equal(calls.length,1);
      assert.equal(calls[0].name,'gateway.chat.assistant-text');
      assert.equal(calls[0].props.text,'synthetic');
      assert.equal(calls[0].props.textInteractionSource,true);
      assert.equal(calls[0].props.presentedFiles,files);
      assert.equal(calls[0].props.fileMentions,mentions);
    } else if (name.endsWith('deliverables')) {
      assert.match(after, /contextMenuProps: getTriggerProps/);
      assert.match(after, /krea-ultra\|moss-image/);
      assert.match(after, /renderSlot\("deliverables.file.actions"/);
      const start=after.indexOf('function PresentedFileCard('),end=after.indexOf('\n\t\t//#endregion',start);
      const Component=vm.runInNewContext(`${after.slice(start,end)}; PresentedFileCard`,{});
      const image={synthetic:true};
      assert.equal(Component({generatedImage:image}),image);
    } else {
      const start = after.indexOf('function Entry({ node, tree })'), end = after.indexOf('\n\t\t/** No entries', start);
      const jsx = (type, props) => ({ type, props });
      const context = { react_jsx_runtime:{ jsx, jsxs:jsx }, react:{Fragment:'fragment'}, clsx:(...x)=>x.join(' '), FileTree_module_css_default:{},
        _deepseek_ai_dsh_client_ui_primitives:{FileTypeIcon:'file',classifyFileType:()=>'',IconFolderCloseRegular:'folder',IconFolderOpenRegular:'folder-open'} };
      const Entry = vm.runInNewContext(`${after.slice(start, end)}; Entry`, context);
      const calls = [], tree = { input:{ renderGatewayActions:(...args)=>{calls.push(args);return 'gateway';}, onOpen:()=>{}, onToggle:()=>{} }, Accessory:'native', expanded:new Set(), t:()=>'' };
      for (const kind of ['file', 'directory', 'other']) Entry({ node:{entry:{path:'/fixture/'+kind,name:kind},kind}, tree });
      assert.deepEqual(calls, [['/fixture/file','file'], ['/fixture/directory','directory']]);
      assert.match(after, /children: \{ "gateway.files.row.actions": \{ kind: "list", scope: "session" \} \}/);
    }
    await patch(root);
    assert.equal(await readFile(file, 'utf8'), after);
    const broken = name.endsWith('chat') ? before.replace('const tail = useTurnData("turn-tail");', '') : name.endsWith('deliverables') ? before.replace('children: { "deliverables.file.actions": {', '') : before.replace('current: location?.current,', '');
    assert.notEqual(broken, before);
    await writeFile(file, broken);
    await assert.rejects(patch(root), /Unexpected rc.1 UI anchor/);
    assert.equal(await readFile(file, 'utf8'), broken);
  });
}
