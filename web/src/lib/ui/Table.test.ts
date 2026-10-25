import { createRawSnippet } from 'svelte';
import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import Table from './Table.svelte';

it('renders the caption, a header cell for each column and the rows', async () => {
  const children = createRawSnippet(() => ({
    render: () => '<tr><td>proxy.example:8080</td><td>429</td></tr>',
  }));
  const screen = await render(Table, {
    caption: 'Proxy cooldowns',
    columns: ['Proxy', 'Reason'],
    children,
  });
  const table = screen.getByRole('table', { name: 'Proxy cooldowns' });
  await expect.element(table).toBeVisible();
  const headers = screen.getByRole('columnheader').elements();
  expect(headers.map((th) => [th.textContent, th.getAttribute('scope')])).toEqual([
    ['Proxy', 'col'],
    ['Reason', 'col'],
  ]);
  const cells = screen.getByRole('cell').elements();
  expect(cells.map((td) => td.textContent)).toEqual(['proxy.example:8080', '429']);
});

it('puts the table in a scroll region that the caption names and the keyboard can focus', async () => {
  const children = createRawSnippet(() => ({ render: () => '<tr><td>a</td></tr>' }));
  const screen = await render(Table, { caption: 'Workers', columns: ['Worker'], children });
  const region = screen.getByRole('region', { name: 'Workers' });
  await expect.element(region).toHaveAttribute('tabindex', '0');
  await expect.element(region.getByRole('table', { name: 'Workers' })).toBeVisible();
});

// The pin and the look of the last header cell and the last body cell, as the browser draws them.
async function lastColumn(props: { stickyActions?: boolean } = {}) {
  const children = createRawSnippet(() => ({
    render: () => '<tr><td>proxy.example:8080</td><td>actions</td></tr>',
  }));
  const screen = await render(Table, {
    caption: 'Proxies',
    columns: ['Proxy', 'Actions'],
    children,
    ...props,
  });
  const table = screen.getByRole('table', { name: 'Proxies' }).element();
  const look = (cell: Element) => {
    const style = getComputedStyle(cell);
    return [style.position, style.right, style.backgroundColor, style.boxShadow];
  };
  const root = getComputedStyle(document.documentElement);
  const color = (token: string) => {
    const probe = document.createElement('span');
    probe.style.color = root.getPropertyValue(token);
    document.body.append(probe);
    const rgb = getComputedStyle(probe).color;
    probe.remove();
    return rgb;
  };
  const cells = screen.getByRole('cell');
  return {
    marked: table.getAttribute('data-sticky-actions'),
    header: look(screen.getByRole('columnheader').last().element()),
    body: look(cells.last().element()),
    first: look(cells.first().element()),
    color,
  };
}

it('pins the last column with an opaque cell and a left separator when asked', async () => {
  const { marked, header, body, first, color } = await lastColumn({ stickyActions: true });
  expect(marked).toBe('true');
  // Tailwind puts empty ring layers before the shadow, so the separator is the last layer.
  const separator = `, ${color('--color-line')} 1px 0px 0px 0px inset`;
  expect(header.slice(0, 3)).toEqual(['sticky', '0px', color('--color-panel')]);
  expect(body.slice(0, 3)).toEqual(['sticky', '0px', color('--color-surface')]);
  for (const cell of [header, body]) {
    expect(cell[3]?.endsWith(separator), cell[3]).toBe(true);
  }
  expect(first).toEqual(['static', 'auto', 'rgba(0, 0, 0, 0)', 'none']);
});

it('pins no column by default', async () => {
  const { marked, header, body } = await lastColumn();
  expect(marked).toBe('false');
  for (const cell of [header, body]) {
    expect(cell).toEqual(['static', 'auto', 'rgba(0, 0, 0, 0)', 'none']);
  }
});
