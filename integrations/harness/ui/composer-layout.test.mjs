import assert from 'node:assert/strict';
import test from 'node:test';
import { composerViewport, installComposerViewport } from './composer-layout.mjs';

test('keyboard layout requires mobile editing and a real height reduction, not pinch zoom',()=>{
  const base={height:400,layoutHeight:844,focused:true,mobile:true};
  assert.deepEqual(composerViewport(base),{height:400,top:0,keyboard:true});
  assert.equal(composerViewport({...base,height:760}).keyboard,false);
  assert.equal(composerViewport({...base,focused:false}).keyboard,false);
  assert.equal(composerViewport({...base,mobile:false}).keyboard,false);
  assert.equal(composerViewport({...base,scale:2}).keyboard,false);
  assert.equal(composerViewport({...base,scale:0.8}).keyboard,false);
  assert.equal(composerViewport({...base,height:260,layoutHeight:500}).keyboard,true);
});

test('viewport geometry preserves offset and safely falls back from invalid measurements',()=>{
  assert.deepEqual(composerViewport({height:380,top:24,layoutHeight:844}),{height:380,top:24,keyboard:false});
  for(const height of [NaN,undefined,0,-1])assert.equal(composerViewport({height,layoutHeight:844}).height,844);
  assert.equal(composerViewport({height:380,top:-10,layoutHeight:844}).top,0);
});

test('one viewport publisher coalesces events, resets orientation baseline and fully cleans up',()=>{
  class Target extends EventTarget {}
  const values=new Map([['--gateway-viewport-height','old-height']]),attributes=new Set();
  const listeners=new Map(),raf=new Map();let sequence=0,writes=0;
  const win=Object.assign(new Target(),{innerWidth:390,innerHeight:844,
    visualViewport:Object.assign(new Target(),{height:844,offsetTop:0,scale:1}),
    requestAnimationFrame(fn){const id=++sequence;raf.set(id,fn);return id;},cancelAnimationFrame:id=>raf.delete(id)});
  const media=Object.assign(new Target(),{matches:true});win.matchMedia=()=>media;
  const doc=Object.assign(new Target(),{activeElement:null,
    documentElement:{clientHeight:844,style:{getPropertyValue:name=>values.get(name)||'',
      setProperty(name,value){writes++;values.set(name,value);},removeProperty:name=>values.delete(name)}},
    body:{hasAttribute:name=>attributes.has(name),toggleAttribute(name,on){if(on)attributes.add(name);else attributes.delete(name);}}});
  for(const [target,name] of [[win,'window'],[win.visualViewport,'viewport'],[doc,'document'],[media,'media']]){
    const add=target.addEventListener.bind(target),remove=target.removeEventListener.bind(target);
    target.addEventListener=(type,fn)=>{listeners.set(`${name}:${type}`,fn);add(type,fn);};
    target.removeEventListener=(type,fn)=>{assert.equal(listeners.get(`${name}:${type}`),fn);listeners.delete(`${name}:${type}`);remove(type,fn);};
  }
  const flush=()=>{for(const [id,fn] of [...raf]){raf.delete(id);fn();}};
  const dispose=installComposerViewport(win,doc);
  assert.equal(values.get('--gateway-viewport-height'),'844px');
  doc.activeElement={closest:()=>({})};win.visualViewport.height=400;win.visualViewport.offsetTop=12;
  win.visualViewport.dispatchEvent(new Event('resize'));doc.dispatchEvent(new Event('focusin'));win.visualViewport.dispatchEvent(new Event('scroll'));
  assert.equal(raf.size,1);flush();
  assert(attributes.has('data-gateway-keyboard-open'));
  assert.equal(values.get('--gateway-viewport-height'),'400px');
  assert.equal(values.get('--gateway-viewport-top'),'12px');
  const stable=writes;win.dispatchEvent(new Event('resize'));flush();assert.equal(writes,stable);
  doc.activeElement=null;win.innerHeight=600;doc.documentElement.clientHeight=600;win.visualViewport.height=600;
  win.dispatchEvent(new Event('resize'));flush();
  doc.activeElement={closest:()=>({})};doc.dispatchEvent(new Event('focusin'));flush();
  assert.equal(attributes.has('data-gateway-keyboard-open'),false,'A same-width window resize must not leave a stale keyboard baseline');
  win.innerWidth=640;win.innerHeight=360;doc.documentElement.clientHeight=360;win.visualViewport.height=360;
  win.dispatchEvent(new Event('resize'));flush();assert.equal(attributes.has('data-gateway-keyboard-open'),false);
  win.dispatchEvent(new Event('resize'));assert.equal(raf.size,1);dispose();
  assert.equal(raf.size,0);assert.equal(listeners.size,0);assert.equal(attributes.size,0);
  assert.equal(values.get('--gateway-viewport-height'),'old-height');assert.equal(values.has('--gateway-viewport-top'),false);
  win.dispatchEvent(new Event('resize'));assert.equal(raf.size,0);
});
