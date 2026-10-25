import { createRawSnippet } from 'svelte';
import { expect, it } from 'vitest';
import { userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import Details from './Details.svelte';

const body = createRawSnippet(() => ({ render: () => '<p>Section body</p>' }));

async function renderDetails(props: { digest?: string; open?: boolean } = {}) {
  const screen = await render(Details, { summary: 'Mailbox', children: body, ...props });
  const group = screen.getByRole('group');
  const summary = () => screen.container.querySelector('summary');
  const shown = (text: string) => screen.getByText(text, { exact: true });
  return { screen, group, summary, shown };
}

it('starts closed by default and opens from the keyboard', async () => {
  const { group, summary, shown } = await renderDetails();
  await expect.element(group).not.toHaveAttribute('open');
  await expect.element(shown('Section body')).not.toBeVisible();
  await userEvent.tab();
  expect(document.activeElement).toBe(summary());
  await userEvent.keyboard('{Enter}');
  await expect.element(group).toHaveAttribute('open');
  await expect.element(shown('Section body')).toBeVisible();
});

it('starts open when asked', async () => {
  const { group, shown } = await renderDetails({ open: true });
  await expect.element(group).toHaveAttribute('open');
  await expect.element(shown('Section body')).toBeVisible();
});

it('keeps its start state when the open prop changes, and follows the user', async () => {
  const { screen, group, summary, shown } = await renderDetails({ open: true });
  await screen.rerender({ open: false });
  await expect.element(group).toHaveAttribute('open');
  await expect.element(shown('Section body')).toBeVisible();
  summary()?.click();
  await expect.element(group).not.toHaveAttribute('open');
  await screen.rerender({ open: true });
  await expect.element(group).not.toHaveAttribute('open');
  summary()?.click();
  await expect.element(group).toHaveAttribute('open');
});

it('shows the digest beside the summary only while the section is closed', async () => {
  const { summary, shown } = await renderDetails({ digest: 'user@example.com at mail:993' });
  const digest = shown('user@example.com at mail:993');
  await expect.element(digest).toBeVisible();
  expect(summary()?.contains(digest.element())).toBe(true);
  summary()?.click();
  await expect.element(shown('Section body')).toBeVisible();
  await expect.element(digest).not.toBeVisible();
});

it('shows no digest when it has none', async () => {
  const { summary } = await renderDetails();
  expect(summary()?.textContent.trim()).toBe('Mailbox');
  // An empty digest span would still take a gap in the summary row.
  expect(summary()?.children).toHaveLength(1);
});

it('shows a disclosure mark that turns when the section opens', async () => {
  const { summary } = await renderDetails();
  const mark = summary()?.querySelector('svg');
  if (mark == null) throw new Error('no mark');
  expect(mark.getAttribute('aria-hidden')).toBe('true');
  expect(getComputedStyle(mark).rotate).toBe('none');
  summary()?.click();
  await expect.poll(() => getComputedStyle(mark).rotate).toBe('90deg');
});
