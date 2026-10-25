import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import ErrorList from './ErrorList.svelte';

it('renders nothing for an empty list', async () => {
  const screen = await render(ErrorList, { name: 'Save errors', messages: [] });
  expect(screen.container.innerHTML).not.toContain('<div');
  expect(screen.getByRole('alert').query()).toBeNull();
});

it('lists each message in a named alert', async () => {
  const screen = await render(ErrorList, {
    name: 'Save errors',
    messages: ['email.imap_host is required', 'email.imap_host is required', 'bad timeout'],
  });
  const alert = screen.getByRole('alert', { name: 'Save errors' });
  await expect.element(alert).toBeVisible();
  const items = alert
    .getByRole('listitem')
    .elements()
    .map((li) => li.textContent);
  expect(items).toEqual([
    'email.imap_host is required',
    'email.imap_host is required',
    'bad timeout',
  ]);
});

it('follows a change of the list', async () => {
  const screen = await render(ErrorList, { name: 'Save errors', messages: ['one'] });
  await expect.element(screen.getByRole('alert', { name: 'Save errors' })).toBeVisible();
  await screen.rerender({ messages: [] });
  await expect.poll(() => screen.getByRole('alert').query()).toBeNull();
});
