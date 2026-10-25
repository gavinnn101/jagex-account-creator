import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import TextArea from './TextArea.svelte';

it('labels the text area and shows its value', async () => {
  const screen = await render(TextArea, { label: 'Proxy lines', value: 'h:1' });
  const area = screen.getByLabelText('Proxy lines');
  await expect.element(area).toHaveValue('h:1');
  expect(area.element()).not.toHaveAttribute('aria-describedby');
  expect(screen.container.querySelector('p')).toBeNull();
  await area.fill('h:1\nh:2');
  await expect.element(area).toHaveValue('h:1\nh:2');
});

it('describes the text area with its hint', async () => {
  const screen = await render(TextArea, { label: 'Proxy lines', hint: 'One per line.' });
  expect(screen.getByLabelText('Proxy lines').element()).toHaveAccessibleDescription(
    'One per line.',
  );
  expect(screen.getByText('One per line.').query()).toBeVisible();
});

it('passes the attributes of the caller to the text area', async () => {
  const screen = await render(TextArea, { label: 'Proxy lines', rows: 6, spellcheck: false });
  const area = screen.getByLabelText('Proxy lines');
  await expect.element(area).toHaveAttribute('rows', '6');
  await expect.element(area).toHaveAttribute('spellcheck', 'false');
});
