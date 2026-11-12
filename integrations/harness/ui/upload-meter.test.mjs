import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createUploadMeter, uploadMetricsText } from './upload-meter.mjs';

test('speed and ETA use real byte progress and decline to zero on a stall',()=>{
  let time=0;const meter=createUploadMeter({now:()=>time});
  assert.equal(meter.sample(0,10000).speed,null);
  time=1000;const first=meter.sample(1000,10000);
  assert.equal(first.speed,1000);assert.equal(first.remaining,9);assert.equal(first.percent,10);
  time=2000;assert.equal(meter.sample(2000,10000).speed,1000);
  for(time=3000;time<=10000;time+=1000)meter.sample(2000,10000);
  const stalled=meter.sample(2000,10000);assert.equal(stalled.speed,0);assert.equal(stalled.remaining,null);
});
test('completed transfer waits for server confirmation, time and size units remain finite',()=>{
  let time=0;const meter=createUploadMeter({now:()=>time});time=1000;
  const complete=meter.sample(100,100);assert.equal(complete.saving,true);assert.equal(complete.remaining,0);
  assert.equal(uploadMetricsText(complete),'Сохраняем файл…');
  assert.equal(uploadMetricsText({saving:false,speed:2097152,remaining:3661}),'2.0 МБ/с · осталось 1:01:01');
  assert.equal(uploadMetricsText({saving:false,speed:null,remaining:null},false),'Measuring speed…');
});
