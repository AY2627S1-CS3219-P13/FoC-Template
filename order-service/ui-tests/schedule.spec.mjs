import {test,expect} from '@playwright/test';
test('Schedule order is a child page with server-provided slots and no expiry editor',async({page})=>{
 await page.request.post('/__test/reset');
 await page.request.get('/__test/session?actor=requester');
 await page.goto('/orders');
 await page.getByRole('link',{name:'Schedule order',exact:true}).click();
 await expect(page).toHaveURL(/\/orders\/schedule$/);
 await expect(page.getByRole('link',{name:'Schedule order',exact:true})).toHaveAttribute('aria-current','page');
 await expect(page.getByRole('button',{name:'New errand',exact:true})).not.toHaveAttribute('aria-current');
 await expect(page.getByRole('link',{name:'Back to orders',exact:true})).toHaveCount(0);
 await expect(page.locator('#preview-status')).toHaveText('Scheduled');
 await expect(page.getByRole('link',{name:'Schedule order',exact:true})).toHaveCSS('background-color','rgb(255, 255, 255)');
 await expect(page.locator('#expiry')).toBeHidden();
 await expect(page.getByLabel('Delivery date')).toBeVisible();
 await page.route('**/api/v1/scheduled-orders/slots?**',route=>route.fulfill({json:{slots:['2030-11-04T09:00:00+08:00','2030-11-04T09:30:00+08:00']}}));
 await page.locator('#pickup').selectOption('location:com2');
 await page.locator('#delivery').selectOption('location:utown');
 await page.getByLabel('Delivery date').fill('2030-11-04');
 await expect(page.locator('#schedule-slot option')).toHaveCount(3);
 await page.locator('#schedule-slot').selectOption('2030-11-04T09:00:00+08:00');
 await page.locator('#item-name').fill('Scheduled tea');
 await page.locator('#item-quantity').fill('1');
 await page.locator('#add-item').click();
 await page.locator('#offer').fill('10');
 let submitted;
 await page.route('**/api/v1/scheduled-orders',async route=>{submitted=route.request().postDataJSON();await route.fulfill({status:201,json:{order:{id:'scheduled-ui',requesterId:'11111111-1111-4111-8111-111111111111',status:'scheduled',version:1}}})});
 await page.locator('#submit').click();
 await expect(page.locator('#form-message')).toContainText('scheduled');
 expect(submitted.windowStart).toBe('2030-11-04T09:00:00+08:00');
 expect(submitted).not.toHaveProperty('expiresAt');
 expect(submitted.items[0].name).toBe('Scheduled tea');
});

test('Scheduled badges are distinct and New errand returns to regular creation',async({page})=>{
 await page.request.post('/__test/reset');
 await page.request.get('/__test/session?actor=requester');
 await page.goto('/orders/schedule');
 const colours=await page.evaluate(()=>{
  const statuses=['scheduled','open','accepted','picked-up','delivered','completed','dispute','not-completed','cancelled','expired'];
  return statuses.map(status=>{
   const badge=document.createElement('span');
   badge.className=`pill status-${status}`;
   document.body.append(badge);
   const colour=getComputedStyle(badge).backgroundColor;
   badge.remove();
   return colour;
  });
 });
 expect(colours.slice(1)).not.toContain(colours[0]);
 const rgb=colour=>colour.match(/\d+/g).map(Number);
 const scheduled=rgb(colours[0]);
 for(const colour of colours.slice(1)){
  const distance=Math.hypot(...rgb(colour).map((channel,index)=>channel-scheduled[index]));
  expect(distance).toBeGreaterThan(45);
 }
 await page.getByRole('button',{name:'My requests',exact:true}).click();
 await expect(page.getByRole('link',{name:'Schedule order',exact:true})).not.toHaveAttribute('aria-current');
 await expect(page.getByRole('button',{name:'My requests',exact:true})).toHaveAttribute('aria-current','page');
 await page.getByRole('button',{name:'New errand',exact:true}).click();
 await expect(page).toHaveURL(/\/orders$/);
 await expect(page.getByRole('button',{name:'New errand',exact:true})).toHaveAttribute('aria-current','page');
 await expect(page.locator('#preview-status')).toHaveText('Open');
});
