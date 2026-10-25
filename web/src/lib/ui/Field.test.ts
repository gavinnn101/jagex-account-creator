import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import Field from './Field.svelte';

it('labels the input and shows its value', async () => {
  const screen = await render(Field, { label: 'IMAP host', value: 'mail.example.com' });
  const input = screen.getByLabelText('IMAP host');
  await expect.element(input).toHaveValue('mail.example.com');
  expect(input.element()).not.toHaveAttribute('aria-describedby');
  expect(screen.container.querySelector('p')).toBeNull();
  await input.fill('imap.example.com');
  await expect.element(input).toHaveValue('imap.example.com');
});

it('describes the input with its hint', async () => {
  const screen = await render(Field, {
    label: 'Chrome timeout',
    hint: 'A Go duration.',
    value: '30s',
  });
  expect(screen.getByLabelText('Chrome timeout').element()).toHaveAccessibleDescription(
    'A Go duration.',
  );
  expect(screen.getByText('A Go duration.').query()).toBeVisible();
});

it('passes the attributes of the caller to the input', async () => {
  const screen = await render(Field, {
    label: 'Max workers',
    type: 'number',
    readonly: true,
    value: 4,
  });
  const input = screen.getByLabelText('Max workers');
  await expect.element(input).toHaveAttribute('type', 'number');
  await expect.element(input).toHaveAttribute('readonly');
  await expect.element(input).toHaveValue(4);
});

// The background and text color of the input of a Field with props, as the browser draws them.
async function look(props: { label: string; type?: string; readonly?: boolean }): Promise<string> {
  const screen = await render(Field, props);
  const style = getComputedStyle(screen.getByLabelText(props.label).element());
  return `${style.backgroundColor} ${style.color}`;
}

it('gives a text and a file input the 32 px height of a button', async () => {
  for (const type of ['text', 'file']) {
    const screen = await render(Field, { label: 'Value', type });
    expect(screen.getByLabelText('Value').element().getBoundingClientRect().height).toBe(32);
    await screen.unmount();
  }
});

it('gives the read-only look to a readonly input only, not to a file input', async () => {
  const plain = await look({ label: 'Plain' });
  expect(await look({ label: 'Database', readonly: true })).not.toBe(plain);
  expect(await look({ label: 'Proxy file', type: 'file' })).toBe(plain);
});
