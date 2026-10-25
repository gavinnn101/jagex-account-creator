import { expect, it, vi } from 'vitest';
import { userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import ConfirmButton from './ConfirmButton.svelte';

async function renderConfirm(props: { size?: 'icon' } = {}) {
  const onconfirm = vi.fn(() => Promise.resolve());
  const screen = await render(ConfirmButton, { name: 'Fast', onconfirm, ...props });
  const button = (name: string) => screen.getByRole('button', { name, exact: true });
  const shown = () =>
    screen
      .getByRole('button')
      .elements()
      .map((b) => [
        b.getAttribute('aria-label'),
        b.getAttribute('title'),
        b.textContent.trim(),
        b.dataset.variant,
        b.dataset.size,
      ]);
  // The lucide name of the icon of a button, for example "trash".
  const icon = (name: string) =>
    [...(button(name).element().querySelector('svg')?.classList ?? [])]
      .filter((c) => c.startsWith('lucide-') && c !== 'lucide-icon')
      .map((c) => c.slice('lucide-'.length));
  // Sends one click with the detail that a pointer or a key press gives.
  const click = (name: string, detail: number) =>
    button(name)
      .element()
      .dispatchEvent(new MouseEvent('click', { bubbles: true, detail }));
  return { onconfirm, button, shown, icon, click };
}

it('shows one Delete button until the first click', async () => {
  const { onconfirm, button, shown } = await renderConfirm();
  await expect.element(button('Delete Fast')).toBeVisible();
  expect(shown()).toEqual([['Delete Fast', 'Delete Fast', 'Delete', 'ghost', 'default']]);
  expect(onconfirm).not.toHaveBeenCalled();
});

it('asks for the confirm, and a cancel goes back with no call', async () => {
  const { onconfirm, button, shown } = await renderConfirm();
  await button('Delete Fast').click();
  await expect.element(button('Confirm delete Fast')).toBeVisible();
  expect(shown()).toEqual([
    ['Cancel delete Fast', 'Cancel delete Fast', 'Cancel', 'secondary', 'default'],
    ['Confirm delete Fast', null, 'Confirm delete', 'danger', 'default'],
  ]);
  await button('Cancel delete Fast').click();
  await expect.element(button('Delete Fast')).toBeVisible();
  expect(button('Confirm delete Fast').query()).toBeNull();
  expect(onconfirm).not.toHaveBeenCalled();
});

it('calls onconfirm one time on the confirm, then shows Delete again', async () => {
  const { onconfirm, button } = await renderConfirm();
  await button('Delete Fast').click();
  await button('Confirm delete Fast').click();
  expect(onconfirm).toHaveBeenCalledOnce();
  await expect.element(button('Delete Fast')).toBeVisible();
  expect(button('Cancel delete Fast').query()).toBeNull();
});

it('keeps the focus on one element, so Enter then Enter cancels', async () => {
  const { onconfirm, button, shown } = await renderConfirm();
  const toggle = button('Delete Fast').element();
  toggle.focus();
  await userEvent.keyboard('{Enter}');
  await expect.element(button('Confirm delete Fast')).toBeVisible();
  expect(document.activeElement).toBe(toggle);
  expect(toggle.getAttribute('aria-label')).toBe('Cancel delete Fast');
  await userEvent.keyboard('{Enter}');
  await expect.element(button('Delete Fast')).toBeVisible();
  expect(document.activeElement).toBe(toggle);
  expect(shown()).toEqual([['Delete Fast', 'Delete Fast', 'Delete', 'ghost', 'default']]);
  expect(onconfirm).not.toHaveBeenCalled();
});

it('ignores the confirm click of a double click', async () => {
  const { onconfirm, button, click } = await renderConfirm();
  click('Delete Fast', 1);
  await expect.element(button('Confirm delete Fast')).toBeVisible();
  click('Confirm delete Fast', 2);
  expect(onconfirm).not.toHaveBeenCalled();
  await expect.element(button('Confirm delete Fast')).toBeVisible();
});

it.for([0, 1])('confirms on a click with detail %d', async (detail) => {
  const { onconfirm, button, click } = await renderConfirm();
  click('Delete Fast', 1);
  await expect.element(button('Confirm delete Fast')).toBeVisible();
  click('Confirm delete Fast', detail);
  expect(onconfirm).toHaveBeenCalledOnce();
  await expect.element(button('Delete Fast')).toBeVisible();
});

it('confirms on Enter on the confirm button', async () => {
  const { onconfirm, button } = await renderConfirm();
  await button('Delete Fast').click();
  button('Confirm delete Fast').element().focus();
  await userEvent.keyboard('{Enter}');
  expect(onconfirm).toHaveBeenCalledOnce();
});

it('shows the trash icon, then the x icon, at the icon size', async () => {
  const { button, shown, icon } = await renderConfirm({ size: 'icon' });
  await expect.element(button('Delete Fast')).toBeVisible();
  expect(shown()).toEqual([['Delete Fast', 'Delete Fast', '', 'ghost', 'icon']]);
  expect(icon('Delete Fast')).toContain('trash');
  expect(button('Delete Fast').element().querySelector('svg')?.getAttribute('aria-hidden')).toBe(
    'true',
  );
  await button('Delete Fast').click();
  await expect.element(button('Confirm delete Fast')).toBeVisible();
  expect(shown()).toEqual([
    ['Cancel delete Fast', 'Cancel delete Fast', '', 'secondary', 'icon'],
    ['Confirm delete Fast', null, 'Confirm delete', 'danger', 'default'],
  ]);
  expect(icon('Cancel delete Fast')).toContain('x');
  expect(icon('Cancel delete Fast')).not.toContain('trash');
});
