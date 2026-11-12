import {createServer,request} from 'node:http';
const key=process.env.HARNESS_TENANT_KEY;
createServer((req,res)=>{
 if(req.headers.authorization!=='Bearer '+key){res.writeHead(403).end();return;}
 res.setHeader('Content-Type','application/json');res.end(req.url.endsWith('/access')?'{"allowed":true}':'{"data":[]}');
}).listen(8099,'127.0.0.1');
createServer((req,res)=>{
 if(!req.url.startsWith('/harness/')){res.writeHead(404).end();return;}
 const headers={...req.headers,authorization:'Bearer '+key,'x-harness-user':'201','accept-encoding':''};delete headers.cookie;
 const upstream=request({hostname:'127.0.0.1',port:8098,path:req.url.slice(8),method:req.method,headers,agent:false},response=>{
  const output={...response.headers};delete output['set-cookie'];
  if(output['content-type']?.startsWith('text/html')){
   let body='';response.setEncoding('utf8');response.on('data',chunk=>body+=chunk);response.on('end',()=>{body=body.replace('</body>','<span id="gateway-upload-retention" hidden data-hours="24"></span></body>');output['content-length']=Buffer.byteLength(body);res.writeHead(response.statusCode,output);res.end(body);});
  }else{res.writeHead(response.statusCode,output);response.pipe(res);}
 });upstream.on('error',()=>{res.writeHead(502).end();});req.pipe(upstream);
}).listen(8096,'0.0.0.0');
