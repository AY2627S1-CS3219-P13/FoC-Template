import {test,expect} from '@playwright/test';

test.beforeEach(async ({page})=>{
  await page.request.post('/__test/reset');
  await page.request.get('/__test/session?actor=requester');
});
async function form(page) {
  await page.goto('/orders');
  await expect(page.locator('#pickup')).toBeEnabled();
  await page.locator('#pickup').selectOption('supplier:22222222-2222-4222-8222-222222222222');
  await page.locator('#delivery').selectOption('location:utown');
}

test('live suggestions follow added quantities and preserve manually chosen credits',async ({page})=>{
  const bodies=[];
  await page.route('**/api/v1/orders/price-quotes',async route=>{
    const body=route.request().postDataJSON();bodies.push(body);
    await route.fulfill({json:{quoteId:'55555555-5555-4555-8555-555555555555',draft:body.draft,recommendedCredits:10+(body.totalItemQuantity||0),validUntil:new Date(Date.now()+120000).toISOString(),releaseId:'local-demo-test'}});
  });
  await form(page);
  await expect(page.locator('#pricing-message')).toContainText('10 credits');
  await expect(page.locator('#expiry')).toHaveValue('');
  await expect(page.locator('#refresh-suggestion')).toHaveCount(0);
  await page.locator('#offer').fill('25');
  await page.locator('#item-name').fill('Tea');
  await page.locator('#item-quantity').fill('4');
  await page.locator('#add-item').click();
  await expect(page.locator('#pricing-message')).toContainText('14 credits');
  await expect(page.locator('#offer')).toHaveValue('25');
  expect(bodies.at(-1).totalItemQuantity).toBe(4);
  await page.locator('#use-suggestion').click();
  await expect(page.locator('#offer')).toHaveValue('14');
  await page.locator('#items button').click();
  await expect(page.locator('#pricing-message')).toContainText('10 credits');
  await expect(page.locator('#offer')).toHaveValue('14');
});

test('unavailable pricing allows manual posting and scheduling never requests quotes',async ({page})=>{
  let calls=0;
  await page.route('**/api/v1/orders/price-quotes',async route=>{calls++;await route.fulfill({status:503,json:{error:{code:'quote_unavailable',message:'Suggestion unavailable'}}});});
  await form(page);
  await expect(page.locator('#pricing-message')).toContainText('Suggestion unavailable');
  await page.locator('#item-name').fill('Manual Tea');await page.locator('#add-item').click();
  await page.locator('#offer').fill('9');
  await page.locator('#expiry').fill(new Date(Date.now()+10*3600000).toISOString().slice(0,16));
  await page.locator('#submit').click();
  await expect(page.locator('#form-message')).toContainText('Errand confirmed');
  const previous=calls;
  await page.goto('/orders/schedule');
  await expect(page.locator('#pricing-suggestion')).toBeHidden();
  expect(calls).toBe(previous);
});


test('suggestions refresh automatically without expiry or an input change',async ({page})=>{
  await page.clock.install();
  const attempts=[];
  await page.route('**/api/v1/orders/price-quotes',async route=>{
    const body=route.request().postDataJSON();
    attempts.push({body,key:route.request().headers()['idempotency-key']});
    await route.fulfill({json:{draft:body.draft,recommendedCredits:10+attempts.length,validUntil:new Date(Date.now()+120000).toISOString()}});
  });
  await form(page);
  await expect(page.locator('#pricing-message')).toContainText('11 credits');
  expect(attempts[0].body.orderExpiresAt).toBeUndefined();
  await page.locator('#offer').fill('24');
  await page.clock.fastForward(30000);
  await expect(page.locator('#pricing-message')).toContainText('12 credits');
  expect(attempts[1].body.draft).toEqual(attempts[0].body.draft);
  expect(attempts[1].key).not.toBe(attempts[0].key);
  await expect(page.locator('#offer')).toHaveValue('24');
});
