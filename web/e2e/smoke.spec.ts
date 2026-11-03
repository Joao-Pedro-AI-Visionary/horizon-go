import { expect, test } from '@playwright/test'

test('home renders brand without console errors', async ({ page }) => {
  const errors: string[] = []
  page.on('console', (msg) => msg.type() === 'error' && errors.push(msg.text()))
  page.on('pageerror', (err) => errors.push(err.message))

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Radar by JpDev' })).toBeVisible()
  await expect(page.getByAltText('JpDev')).toBeVisible()
  expect(errors).toEqual([])
})
