import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import Secret from './Secret.svelte';

afterEach(() => {
  vi.restoreAllMocks();
});

const longValue = 'A'.repeat(200);

async function renderSecret(value = 'S3cret!pass') {
  const screen = await render(Secret, { label: 'password', value });
  const button = (name: string) => screen.getByRole('button', { name, exact: true });
  // The lucide name of the icon of a button, for example "eye-off".
  const icon = (name: string) =>
    [...(button(name).element().querySelector('svg')?.classList ?? [])]
      .filter((c) => c.startsWith('lucide-') && c !== 'lucide-icon')
      .map((c) => c.slice('lucide-'.length));
  return { screen, button, icon };
}

describe('Secret', () => {
  it('hides the value until a reveal, and hides it again', async () => {
    const { screen, button, icon } = await renderSecret();
    await expect.element(screen.getByText('Hidden', { exact: true })).toBeVisible();
    expect(screen.container.textContent).not.toContain('S3cret!pass');
    expect(icon('Reveal password')).toEqual(['eye']);

    await button('Reveal password').click();
    await expect.element(screen.getByText('S3cret!pass', { exact: true })).toBeVisible();
    await expect.element(button('Hide password')).toHaveAttribute('title', 'Hide password');
    expect(icon('Hide password')).toEqual(['eye-off']);

    await button('Hide password').click();
    await expect.element(screen.getByText('Hidden', { exact: true })).toBeVisible();
    await expect.element(button('Reveal password')).toHaveAttribute('title', 'Reveal password');
    expect(screen.container.textContent).not.toContain('S3cret!pass');
  });

  it('shows square icon buttons with no text', async () => {
    const { button, icon } = await renderSecret();
    for (const name of ['Reveal password', 'Copy password']) {
      await expect.element(button(name)).toHaveAttribute('data-size', 'icon');
      expect(button(name).element().textContent.trim()).toBe('');
      expect(button(name).element().querySelector('svg')?.getAttribute('aria-hidden')).toBe('true');
    }
    expect(icon('Copy password')).toEqual(['copy']);
    await expect.element(button('Copy password')).toHaveAttribute('title', 'Copy password');
  });

  it('keeps a long value and the buttons on one row in a narrow box', async () => {
    const { screen, button } = await renderSecret(longValue);
    screen.container.style.width = '200px';
    await button('Reveal password').click();
    const value = screen.getByText(longValue, { exact: true }).element();
    const top = (el: Element) => el.getBoundingClientRect().top;
    await expect.poll(() => value.getBoundingClientRect().width).toBeLessThanOrEqual(200);
    expect(value.getBoundingClientRect().height).toBeGreaterThan(40);
    const hide = button('Hide password').element();
    const copy = button('Copy password').element();
    expect(top(copy)).toBe(top(hide));
    expect(hide.getBoundingClientRect().left).toBeGreaterThan(value.getBoundingClientRect().left);
  });

  it('copies the value, also while it is hidden', async () => {
    const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    const { screen, button } = await renderSecret();
    await expect.element(screen.getByRole('status')).toHaveTextContent('');

    await button('Copy password').click();
    await expect.element(screen.getByRole('status')).toHaveTextContent('Copied');
    expect(write).toHaveBeenCalledExactlyOnceWith('S3cret!pass');
    expect(screen.container.textContent).not.toContain('S3cret!pass');
  });

  it('reports a refused copy', async () => {
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'));
    const { screen, button } = await renderSecret();
    await button('Copy password').click();
    await expect.element(screen.getByRole('status')).toHaveTextContent('Copy failed');
  });

  it('shows no copy button with no clipboard', async () => {
    // An insecure context has no clipboard property at all.
    const clipboard = Object.getOwnPropertyDescriptor(Navigator.prototype, 'clipboard');
    if (clipboard === undefined) throw new Error('no clipboard property');
    Reflect.deleteProperty(Navigator.prototype, 'clipboard');
    try {
      const { screen, button } = await renderSecret();
      await expect.element(button('Reveal password')).toBeVisible();
      expect(screen.getByRole('button').elements()).toHaveLength(1);
      expect(screen.container.querySelector('[role="status"]')).toBeNull();
    } finally {
      Object.defineProperty(Navigator.prototype, 'clipboard', clipboard);
    }
  });
});
