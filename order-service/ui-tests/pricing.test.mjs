import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createSuggestions} from '../internal/order/web/pricing.mjs';
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const inputs = quantity => ({pickup:{type:'location',id:'com2'},delivery:{type:'location',id:'utown'},totalItemQuantity:quantity});
const result = (body, credits=12) => ({quoteId:'test',draft:body.draft,recommendedCredits:credits,validUntil:new Date(Date.now()+120000).toISOString()});

test('debounces edits, fences late responses and keeps a stable draft with new revisions',async () => {
  const requests=[],states=[];
  const controller=createSuggestions({debounceMs:5,request:(body,key,signal)=>new Promise(resolve=>requests.push({body,key,signal,resolve})),render:value=>states.push(value)});
  try {
    controller.update(inputs(1));controller.update(inputs(2));
    await pause(25);assert.equal(requests.length,1);
    const first=requests[0];
    controller.update(inputs(4));await pause(25);
    assert.equal(requests.length,2);const second=requests[1];
    assert.equal(first.signal.aborted,true);
    assert.equal(first.body.draft.id,second.body.draft.id);
    assert.ok(second.body.draft.revision>first.body.draft.revision);
    assert.notEqual(first.key,second.key);
    second.resolve(result(second.body,15));await pause(0);
    first.resolve(result(first.body,7));await pause(0);
    assert.equal(controller.current().recommendedCredits,15);
    assert.equal(states.at(-1).quote.recommendedCredits,15);
    controller.update(null);assert.equal(controller.current(),null);
  } finally {controller.reset();}
});

test('mismatched responses become unavailable; reset discards pending work',async () => {
  let resolve;const states=[];
  const controller=createSuggestions({debounceMs:1,request:body=>new Promise(r=>{resolve=()=>r({...result(body),draft:{...body.draft,revision:99}})}),render:value=>states.push(value)});
  try {
    controller.update(inputs(1));await pause(20);resolve();await pause(0);
    assert.equal(states.at(-1).state,'unavailable');assert.equal(controller.current(),null);
    controller.update(inputs(2));await pause(20);controller.reset();resolve();await pause(0);
    assert.equal(states.at(-1).state,'incomplete');
  } finally {controller.reset();}
});


test('periodic calculations use fresh keys with the same draft and stop on reset',async () => {
  const requests=[];
  const controller=createSuggestions({debounceMs:1,refreshMs:20,request:async(body,key)=>{requests.push({body,key});return result(body)},render:()=>{}});
  try {
    controller.update(inputs(1));
    for(let i=0;i<40 && requests.length<2;i++) await pause(5);
    assert.ok(requests.length>=2);
    assert.deepEqual(requests[0].body.draft,requests[1].body.draft);
    assert.notEqual(requests[0].key,requests[1].key);
    assert.equal('orderExpiresAt' in requests[0].body,false);
    controller.reset();const count=requests.length;await pause(50);
    assert.equal(requests.length,count);
  } finally {controller.reset();}
});

test('failures recover automatically and rate-limit delays are respected',async () => {
  let calls=0;const states=[];
  const controller=createSuggestions({debounceMs:1,refreshMs:10,request:async body=>{
    calls++;
    if(calls===1) throw Object.assign(new Error('rate limited'),{retryAfterMs:100});
    return result(body);
  },render:state=>states.push(state)});
  try {
    controller.update(inputs(1));await pause(40);
    assert.equal(calls,1);assert.equal(states.at(-1).state,'unavailable');
    for(let i=0;i<40 && calls<2;i++) await pause(5);
    assert.ok(calls>=2);assert.equal(states.at(-1).state,'ready');
  } finally {controller.reset();}
});
