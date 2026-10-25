import { createRawSnippet } from 'svelte';
import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import Button from './Button.svelte';

const label = createRawSnippet(() => ({ render: () => '<span>Go</span>' }));

const variants = ['primary', 'secondary', 'ghost', 'danger'] as const;

it('renders a button that never submits and passes each click', async () => {
  let clicks = 0;
  const screen = await render(Button, {
    variant: 'primary',
    children: label,
    onclick: () => {
      clicks += 1;
    },
  });
  const button = screen.getByRole('button', { name: 'Go' });
  await expect.element(button).toHaveAttribute('type', 'button');
  await button.click();
  expect(clicks).toBe(1);
});

it('passes the attributes of the caller', async () => {
  const screen = await render(Button, {
    variant: 'secondary',
    children: label,
    disabled: true,
    title: 'not now',
    'aria-pressed': true,
  });
  const button = screen.getByRole('button', { name: 'Go' });
  await expect.element(button).toBeDisabled();
  await expect.element(button).toHaveAttribute('title', 'not now');
  await expect.element(button).toHaveAttribute('aria-pressed', 'true');
});

it.for(variants)('marks the %s variant and the default size', async (variant) => {
  const screen = await render(Button, { variant, children: label });
  const button = screen.getByRole('button', { name: 'Go' });
  await expect.element(button).toHaveAttribute('data-variant', variant);
  await expect.element(button).toHaveAttribute('data-size', 'default');
});

it('marks the icon size', async () => {
  const screen = await render(Button, { variant: 'ghost', size: 'icon', children: label });
  await expect
    .element(screen.getByRole('button', { name: 'Go' }))
    .toHaveAttribute('data-size', 'icon');
});

// The look of each button, as the browser draws it.
async function looks(disabled: boolean): Promise<string[]> {
  const shown = [];
  for (const variant of variants) {
    const screen = await render(Button, { variant, disabled, children: label });
    const style = getComputedStyle(screen.getByRole('button', { name: 'Go' }).element());
    shown.push(`${style.backgroundColor} ${style.color} ${style.borderColor}`);
    await screen.unmount();
  }
  return shown;
}

it('gives each variant its own look, and every disabled variant the one disabled look', async () => {
  const enabled = await looks(false);
  expect(new Set(enabled).size).toBe(variants.length);
  const disabled = await looks(true);
  expect(new Set(disabled).size).toBe(1);
  expect(enabled).not.toContain(disabled[0]);
});
