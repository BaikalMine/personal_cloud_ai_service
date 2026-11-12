import test from 'node:test';
import assert from 'node:assert/strict';
import {normalizeMemorySources} from './chat-memory-sources.mjs';

test('source descriptors are bounded metadata with canonical cloned fields',()=>{
  const input=[{anchor:'Exact synthetic passage',line_to:25,file:'notes/chapter.md',line_from:20,chapter:'Chapter 2',sha256:'a'.repeat(64)}];
  const result=normalizeMemorySources(input);
  assert.deepEqual(Object.keys(result[0]),['file','chapter','line_from','line_to','anchor','sha256']);
  assert.deepEqual(result,input);input[0].file='changed.md';assert.equal(result[0].file,'notes/chapter.md');
  assert.deepEqual(normalizeMemorySources([]),[]);
  assert.deepEqual(normalizeMemorySources([{file:'notes.md'}]),[{file:'notes.md'}]);
});

test('rejects invalid, absolute, traversal and active source references without reading files',()=>{
  for(const file of ['',null,'/workspace/file.md','C:/file.md','file://test','https://example.com','../file.md','a/../file.md','a/./file.md','a//file.md','a\\file.md','a\u0000.md','a\n.md','x'.repeat(513)])assert.throws(()=>normalizeMemorySources([{file}]),/invalid_sources/);
  for(const input of [null,{},'file',new Array(1),Array(5).fill({file:'file.md'}),[{file:'file.md',unknown:true}],[{file:'file.md',chapter:''}],[{file:'file.md',chapter:'x'.repeat(129)}],[{file:'file.md',anchor:'x'.repeat(513)}],[{file:'file.md',sha256:'A'.repeat(64)}]])assert.throws(()=>normalizeMemorySources(input),/invalid_sources/);
});

test('only an exact trusted workspace prefix may be stripped, without normalizing traversal',()=>{
  const relative='\u041f\u0430\u043f\u043a\u0430 \u043f\u0440\u043e\u0435\u043a\u0442\u0430/notes/chapter.md';
  const options={workspaceRoot:'/workspaces/synthetic/'};
  const lines={line_from:12,line_to:20};
  assert.deepEqual(normalizeMemorySources([{file:'/workspaces/synthetic/'+relative,...lines}],options),[{file:relative,...lines}]);
  assert.deepEqual(normalizeMemorySources([{file:relative,...lines}],options),[{file:relative,...lines}]);
  for(const file of ['/workspaces/synthetic-other/file.md','/workspaces/synthetic','/workspaces/synthetic/',
    '/workspaces/synthetic//file.md','/workspaces/synthetic/../file.md','/workspaces/synthetic/a/./file.md',
    'file:///workspaces/synthetic/file.md','C:/workspaces/synthetic/file.md','/etc/passwd']){
    assert.throws(()=>normalizeMemorySources([{file}],options),/invalid_sources/);
  }
  assert.throws(()=>normalizeMemorySources([{file:'/etc/passwd'}],{workspaceRoot:'/'}),/invalid_sources/);
});

test('line ranges are observed one-based bounded integers and require a start',()=>{
  for(const range of [{line_from:0},{line_from:'1'},{line_from:1.1},{line_from:1_000_000_001},{line_to:2},{line_from:2,line_to:1}])assert.throws(()=>normalizeMemorySources([{file:'file.md',...range}]),/invalid_sources/);
  assert.deepEqual(normalizeMemorySources([{file:'file.md',line_from:1,line_to:1_000_000_000}]),[{file:'file.md',line_from:1,line_to:1_000_000_000}]);
});

test('source budget includes JSON escaping and all descriptors combined',()=>{
  const source={file:'x'.repeat(512),chapter:'y'.repeat(128),anchor:'z'.repeat(512)};
  assert.throws(()=>normalizeMemorySources(Array(4).fill(source)),/invalid_sources/);
  assert.throws(()=>normalizeMemorySources(Array(4).fill({file:'x.md',anchor:'\u0001'.repeat(512)})),/invalid_sources/);
  assert.equal(normalizeMemorySources([{file:'x.md',anchor:'\u{1f600}'.repeat(128)}]).length,1);
});
