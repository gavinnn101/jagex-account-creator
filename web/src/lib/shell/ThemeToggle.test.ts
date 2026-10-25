import { afterEach, expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import ThemeToggle from './ThemeToggle.svelte';
import { themeKey } from './theme.ts';

const root = document.documentElement;

afterEach(() => {
  delete root.dataset.theme;
  localStorage.clear();
});

async function renderToggle() {
  const screen = await render(ThemeToggle);
  const button = screen.getByRole('button', { name: 'Light theme' });
  await expect.element(button).toBeVisible();
  await expect.element(button).toHaveAttribute('type', 'button');
  await expect.element(button).toHaveAttribute('title', 'Light theme');
  await expect.element(button).toHaveAttribute('data-variant', 'ghost');
  await expect.element(button).toHaveAttribute('data-size', 'icon');
  return button;
}

// The names of the icons in the button. The icon shows the theme on screen.
function icon(): string {
  return ['sun', 'moon']
    .filter((name) => document.querySelector(`button svg.lucide-${name}`) !== null)
    .join();
}

it('starts from the attribute, not from storage', async () => {
  localStorage.setItem(themeKey, 'light');
  const button = await renderToggle();
  await expect.element(button).toHaveAttribute('aria-pressed', 'false');
  expect(icon()).toBe('moon');
});

it('starts pressed when the head script showed light', async () => {
  root.dataset.theme = 'light';
  const button = await renderToggle();
  await expect.element(button).toHaveAttribute('aria-pressed', 'true');
  expect(icon()).toBe('sun');
});

it('switches the theme on each click and stores it', async () => {
  const button = await renderToggle();
  await button.click();
  await expect.element(button).toHaveAttribute('aria-pressed', 'true');
  expect(root.dataset.theme).toBe('light');
  expect(localStorage.getItem(themeKey)).toBe('light');
  expect(icon()).toBe('sun');
  await button.click();
  await expect.element(button).toHaveAttribute('aria-pressed', 'false');
  expect(root.dataset.theme).toBe('dark');
  expect(localStorage.getItem(themeKey)).toBe('dark');
  expect(icon()).toBe('moon');
});
