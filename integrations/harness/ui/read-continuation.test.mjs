import test from 'node:test';
import assert from 'node:assert/strict';
import {buildReadWindow,formatReadWindow,readColumn} from './read-continuation.mjs';
const caps={offset:1,limit:2000,maxLineLength:2000,maxBytes:51200};
function chunks(text,size) {return Array.from({length:Math.ceil(text.length/size)},(_,i)=>text.slice(i*size,(i+1)*size));}
async function collect(source,overrides={},chunkSize=73) {
  let cursor={offset:1,columnOffset:0};const results=new Map();let calls=0;
  while(true){
    assert.ok(++calls<2000,'cursor failed to progress');
    const value=await buildReadWindow(chunks(source,chunkSize),{...caps,...overrides,...cursor});
    assert.ok(value.lines.reduce((n,line)=>n+Buffer.byteLength(line.text),0)+Math.max(0,value.lines.length-1)<= (overrides.maxBytes??caps.maxBytes));
    for(const line of value.lines)results.set(line.number,(results.get(line.number)??'')+line.text);
    if(value.complete)break;
    assert.ok(value.nextOffset>cursor.offset||value.nextOffset===cursor.offset&&value.nextColumnOffset>cursor.columnOffset);
    cursor={offset:value.nextOffset,columnOffset:value.nextColumnOffset};
  }
  const expected=source?source.replace(/\r(?=\n|$)/g,'').replace(/\n$/,'').split('\n'):[];
  assert.deepEqual([...results.values()],expected);
  return calls;
}
test('long paragraphs are recoverable without gaps or false EOF',async()=>{
  const source='a'.repeat(8413)+'TAIL';
  const value=await buildReadWindow([source],caps);
  assert.equal(value.complete,false);assert.equal(value.nextOffset,1);assert.equal(value.nextColumnOffset,2000);
  assert.ok(!formatReadWindow('/synthetic',value).includes('End of file'));
  assert.equal(await collect(source),5);
});
test('byte cap, line cap, blank lines and CRLF preserve every character',async()=>{
  for (const text of ['', '\n','\n\n','abc\r\n\r\ndef\r\n','x'.repeat(5000)+'\n\nend','one\ntwo\nthree','0123456789\n\n']) {
    await collect(text,{maxBytes:10,maxLineLength:7,limit:2},1);
  }
});
test('UTF-8 and surrogate pairs survive tiny chunk boundaries and byte caps',async()=>{
  const text=('\u0416\u0438\u0437\u043d\u044c\u{1f600} ').repeat(100)+'\nEND';
  await collect(text,{maxBytes:19,maxLineLength:9},1);
  await collect(text,{maxBytes:51200,maxLineLength:2000},2001);
});
test('large multiline file reads to the actual end',async()=>{
  await collect(('paragraph '.repeat(23)+'\n').repeat(1000),{maxBytes:51200},1024);
});
test('invalid cursor and abort do not return successful read',async()=>{
  for(const value of [-1,.1,NaN,Infinity,Number.MAX_SAFE_INTEGER+1]) assert.throws(()=>readColumn(value));
  await assert.rejects(buildReadWindow(['abc'],{...caps,columnOffset:4}));
  await assert.rejects(buildReadWindow(['\u{1f600}'],{...caps,columnOffset:1}));
  await assert.rejects(buildReadWindow(['abc'],{...caps,offset:2}));
  await assert.rejects(buildReadWindow(['abc'],{...caps,signal:AbortSignal.abort()}));
});
test('old stored outputs are never certified as complete',()=>{
  const output=formatReadWindow('/synthetic',{lines:[{number:1,text:'partial'}],totalLines:1});
  assert.ok(output.includes('completeness was not recorded'));assert.ok(!output.includes('End of file'));
});
