import { createServer } from 'node:http';
let calls = 0, cancelled = 0, fileSeen = false, toolResultSeen = false;
createServer(async (req, res) => {
  if (req.url === '/metrics') { res.setHeader('Content-Type','application/json'); res.end(JSON.stringify({ calls, cancelled, fileSeen, toolResultSeen })); return; }
  if (req.url === '/v1/models') { res.setHeader('Content-Type','application/json'); res.end(JSON.stringify({data:[{id:'fixture-model',object:'model',owned_by:'fixture'}]})); return; }
  if (req.url !== '/v1/chat/completions' || req.method !== 'POST') { res.writeHead(404).end(); return; }
  let raw = ''; for await (const chunk of req) raw += chunk;
  calls++; fileSeen ||= raw.includes('verification-note.txt');
  const input = JSON.parse(raw);
  if (raw.includes('tool-fixture')) {
    const result = input.messages.findLast(m=>m.role === 'tool');
    toolResultSeen ||= !!result && JSON.stringify(result.content).includes('harness-tool-verification');
    res.writeHead(200, {'Content-Type':'text/event-stream','Cache-Control':'no-store'});
    const delta = result ? {role:'assistant',content:toolResultSeen ? 'Harness tool verification complete.' : 'Harness tool verification failed.'} : {role:'assistant',tool_calls:[{index:0,id:'call-fixture',type:'function',function:{name:'bash',arguments:JSON.stringify({description:'Verify isolated workspace command execution',command:'printf harness-tool-verification'})}}]};
    res.end(`data: ${JSON.stringify({id:'fixture-tool',object:'chat.completion.chunk',created:1,model:input.model,choices:[{index:0,delta,finish_reason:result?'stop':'tool_calls'}]})}\n\ndata: [DONE]\n\n`);
    return;
  }
  const slow = raw.includes('cancel-fixture');
  const parts = slow ? Array(100).fill('Streaming ') : ['Harness ', 'file ', 'verification ', 'complete.'];
  res.writeHead(200, {'Content-Type':'text/event-stream','Cache-Control':'no-store'});
  let completed = false;
  res.on('close', () => { if (!completed) cancelled++; });
  for (const part of parts) {
    if (res.destroyed) return;
    res.write(`data: ${JSON.stringify({id:'fixture',object:'chat.completion.chunk',created:1,model:input.model,choices:[{index:0,delta:{role:'assistant',content:part},finish_reason:null}]})}\n\n`);
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  completed = true;
  res.end(`data: ${JSON.stringify({id:'fixture',object:'chat.completion.chunk',created:1,model:input.model,choices:[{index:0,delta:{},finish_reason:'stop'}],usage:{prompt_tokens:100,completion_tokens:10,total_tokens:110}})}\n\ndata: [DONE]\n\n`);
}).listen(3099, '0.0.0.0');
