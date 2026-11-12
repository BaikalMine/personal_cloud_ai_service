import assert from 'node:assert/strict';
import test from 'node:test';
import { reconcilePresentedText } from './assistant-format.mjs';

const path='/workspace/generated/generated/krea-ultra/eef45bbd-1168-43dd-ae8d-a6b5e3ecf673/1.png';
const file={path,width:2144,height:1200,description:'Server-owned description'};
const code=`<tool_code> present(files=${JSON.stringify([{path}])}) </tool_code>`;

test('a textual present is only reconciled with a real delivery in this turn',()=>{
  const result=reconcilePresentedText(`Done.\n${code}\nOther answer.`,[file]);
  assert.equal(result.text,'Done.\n\nOther answer.');
  assert.deepEqual(result.files,[file]); assert.equal(result.malformed,false);
  assert.equal(reconcilePresentedText(code,[]).text,code);
  assert.equal(reconcilePresentedText(code,[]).malformed,true);
});

test('presentation recovery never evaluates code or trusts claimed paths and metadata',()=>{
  for(const body of ['bash(command="delete files")','present(files=__import__("os").environ)',`present(files=[{"path":"/etc/passwd"}])`,`present(files=[{"path":"${path}","url":"https://external.invalid"}])`]) {
    const text=`<tool_code>${body}</tool_code>`,result=reconcilePresentedText(text,[file]);
    assert.equal(result.text,text); assert.deepEqual(result.files,[]); assert.equal(result.malformed,true);
  }
  const result=reconcilePresentedText(`<tool_code>present(files=${JSON.stringify([{path,description:'Not authoritative'}])})</tool_code>`,[file]);
  assert.equal(result.files[0].description,file.description);
});

test('a delivery after a fenced example is still recoverable',()=>{
  const example=`\`\`\`python\n${code}\n\`\`\``;
  const result=reconcilePresentedText(`${example}\n${code}`,[file]);
  assert.equal(result.text,`${example}\n`);assert.deepEqual(result.files,[file]);
});

test('quoted examples, fenced code and ordinary text are unchanged',()=>{
  for(const text of ['Ordinary reply.',`\`${code}\``,`\`\`\`python\n${code}\n\`\`\``,`~~~text\n${code}\n~~~`,`\`\`\`\n${code}`]) {
    assert.deepEqual(reconcilePresentedText(text,[file]),{text,files:[],malformed:false});
  }
});

test('MOSS presentation uses only real turn deliverables, just like Krea2',()=>{
  const path='/workspace/generated/moss-image/eef45bbd-1168-43dd-ae8d-a6b5e3ecf673/1.png';
  const file={path,description:'MOSS Image 1'},code=`<tool_code>present(files=${JSON.stringify([{path}])})</tool_code>`;
  assert.deepEqual(reconcilePresentedText(code,[file]).files,[file]);
  assert.deepEqual(reconcilePresentedText(code,[]).files,[]);
});
