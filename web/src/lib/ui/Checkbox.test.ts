import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import Checkbox from './Checkbox.svelte';

it('labels the checkbox and toggles it', async () => {
  const screen = await render(Checkbox, { label: 'Headless', checked: true });
  const box = screen.getByRole('checkbox', { name: 'Headless' });
  await expect.element(box).toBeChecked();
  expect(box.element()).not.toHaveAttribute('aria-describedby');
  expect(screen.container.querySelector('p')).toBeNull();
  await box.click();
  await expect.element(box).not.toBeChecked();
});

it('describes the checkbox with its hint', async () => {
  const screen = await render(Checkbox, { label: 'Direct', hint: 'No proxy.', checked: false });
  expect(screen.getByRole('checkbox', { name: 'Direct' }).element()).toHaveAccessibleDescription(
    'No proxy.',
  );
  expect(screen.getByText('No proxy.').query()).toBeVisible();
});
