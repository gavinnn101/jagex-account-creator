import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import Select from './Select.svelte';

const options = [
  { value: '', text: 'Any' },
  { value: '1', text: 'Worker 1' },
];

it('labels the select and lists its options in order', async () => {
  const screen = await render(Select, { label: 'Worker', options, value: '1' });
  const select = screen.getByLabelText('Worker');
  await expect.element(select).toHaveValue('1');
  const shown = screen
    .getByRole('option')
    .elements()
    .map((o) => [(o as HTMLOptionElement).value, o.textContent]);
  expect(shown).toEqual([
    ['', 'Any'],
    ['1', 'Worker 1'],
  ]);
});

it('gives the select its field name for a form submit', async () => {
  const screen = await render(Select, { label: 'Worker', options, name: 'worker', value: '1' });
  const select = screen.getByLabelText('Worker').element() as HTMLSelectElement;
  expect(select.name).toBe('worker');
});

it('gives the select the 32 px height of a button', async () => {
  const screen = await render(Select, { label: 'Worker', options });
  expect(screen.getByLabelText('Worker').element().getBoundingClientRect().height).toBe(32);
});

it('takes the chosen value', async () => {
  const screen = await render(Select, { label: 'Worker', options, value: '1' });
  const select = screen.getByLabelText('Worker');
  await select.selectOptions('Any');
  await expect.element(select).toHaveValue('');
});
