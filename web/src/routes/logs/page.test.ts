import '../../app.css';
import { tick } from 'svelte';
import { afterEach, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { lastSource, stubEventSource } from '#lib/streamtest/streamtest.ts';
import Page from './+page.svelte';

function line(runId: number, level: string, msg: string, attrs: Record<string, string> = {}) {
  return {
    run_id: runId,
    time: '2026-10-21T10:00:00.000+02:00',
    level,
    msg,
    attrs: Object.entries(attrs).map(([key, value]) => ({ key, value })),
  };
}

const run = [
  line(4, 'INFO', 'run started', { run_id: '4', source: 'serve' }),
  line(4, 'DEBUG', 'navigating', { component: 'browser', worker: '1', account: '1' }),
  line(4, 'INFO', 'starting account creation', { worker: '2', account: '2' }),
  line(4, 'INFO', 'starting account creation', { worker: '1', account: '1' }),
  line(4, 'INFO', 'account created', { worker: '1', account: '1', email: 'a@example.com' }),
  line(4, 'WARN', 'proxy cooldown', { worker: '2', account: '2' }),
];

afterEach(() => {
  vi.unstubAllGlobals();
});

async function renderPage() {
  stubEventSource();
  const screen = await render(Page);
  const source = lastSource();
  const count = screen.getByText(/^(\d+ of )?\d+ lines?$/);
  const msgs = () =>
    screen
      .getByRole('listitem')
      .elements()
      .map((li) => li.querySelector('span:nth-child(3)')?.textContent);
  const clearButton = screen.getByRole('button', { name: 'Clear', exact: true });
  return { screen, source, count, msgs, clearButton };
}

async function emitRun() {
  const page = await renderPage();
  for (const l of run) page.source.emit(l);
  await expect.element(page.count).toHaveTextContent('6 lines');
  return page;
}

function value(screen: Awaited<ReturnType<typeof renderPage>>['screen'], label: string): string {
  return (screen.getByLabelText(label).element() as HTMLInputElement | HTMLSelectElement).value;
}

it('waits for the first line of a run', async () => {
  const { screen, source, count } = await renderPage();
  await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Logs');
  expect(source.url).toBe('/api/logs');
  const empty = screen.getByText(/^No run log yet\./);
  await expect.element(empty).toBeVisible();
  expect(empty.element().textContent.replace(/\s+/g, ' ').trim()).toBe(
    'No run log yet. Start a run, or get a past log from Runs.',
  );
  const links = screen
    .getByRole('link')
    .elements()
    .map((a) => [a.textContent, a.getAttribute('href')]);
  expect(links).toEqual([
    ['Start a run', '/'],
    ['Runs', '/runs'],
  ]);
  expect(count.elements()).toHaveLength(0);
  expect(screen.container.querySelector('form')).toBeNull();
  expect(screen.container.querySelector('[role="region"]')).toBeNull();
  expect(screen.container.querySelector('[role="status"]')).toBeNull();
});

it('shows the lines of the run and links its detail and its log file', async () => {
  const { screen, msgs } = await emitRun();
  expect(msgs()).toEqual(run.map((l) => l.msg));
  await expect
    .element(screen.getByRole('link', { name: 'Run 4', exact: true }))
    .toHaveAttribute('href', '/runs/4');
  const link = screen.getByRole('link', { name: 'Download the log file' });
  await expect.element(link).toHaveAttribute('href', '/api/runs/4/log');
  await expect.element(link).toHaveAttribute('download');
  await expect.element(link).toHaveAttribute('rel', 'external');
  expect(screen.getByText(/^No run log yet\./).elements()).toHaveLength(0);
  const chosen = ['Level', 'Component', 'Worker'].map(
    (label) =>
      (screen.getByLabelText(label).element() as HTMLSelectElement).selectedOptions[0]?.text,
  );
  expect(chosen).toEqual(['Any', 'Any', 'Any']);
});

it('marks the stream stale until the next line', async () => {
  const { screen, source } = await renderPage();
  const status = () => screen.container.querySelector('[role="status"]')?.textContent ?? null;
  // The fake stream calls the handler before it returns, so one tick shows each change.
  source.fail();
  await tick();
  expect(status()).toBe('Reconnecting. The lines below can be incomplete.');
  source.emit(run[0] ?? line(4, 'INFO', 'x'));
  await tick();
  expect(status()).toBeNull();
});

it('starts with the more filters closed', async () => {
  const { screen } = await emitRun();
  const more = screen.container.querySelector('details');
  expect(more?.querySelector('summary')?.textContent.trim()).toBe('More filters');
  expect(more?.open).toBe(false);
  const inMore = ['Component', 'Worker', 'Account', 'Email'].map((label) =>
    more?.contains(screen.getByLabelText(label).element()),
  );
  expect(inMore).toEqual([true, true, true, true]);
  expect(more?.contains(screen.getByLabelText('Level').element())).toBe(false);
  expect(more?.contains(screen.getByLabelText('Text').element())).toBe(false);
});

it('filters the lines by each field', async () => {
  const { screen, count, msgs } = await emitRun();
  await screen.getByText('More filters').click();

  const level = screen.getByLabelText('Level');
  const levelTexts = level
    .getByRole('option')
    .elements()
    .map((o) => o.textContent);
  expect(levelTexts).toEqual(['Any', 'DEBUG', 'INFO', 'WARN', 'ERROR']);
  await level.selectOptions('WARN');
  await expect.element(count).toHaveTextContent('1 of 6 lines');
  expect(msgs()).toEqual(['proxy cooldown']);
  await level.selectOptions('Any');

  const text = screen.getByLabelText('Text');
  await text.fill('COOLDOWN');
  await expect.element(count).toHaveTextContent('1 of 6 lines');
  await text.fill('');

  const component = screen.getByLabelText('Component');
  const componentTexts = component
    .getByRole('option')
    .elements()
    .map((o) => o.textContent);
  expect(componentTexts).toEqual(['Any', 'browser']);
  await component.selectOptions('browser');
  await expect.element(count).toHaveTextContent('1 of 6 lines');
  expect(msgs()).toEqual(['navigating']);
  await component.selectOptions('Any');

  const worker = screen.getByLabelText('Worker');
  const workerTexts = worker
    .getByRole('option')
    .elements()
    .map((o) => o.textContent);
  expect(workerTexts).toEqual(['Any', '1', '2']);
  await worker.selectOptions('2');
  await expect.element(count).toHaveTextContent('2 of 6 lines');
  await worker.selectOptions('Any');

  const account = screen.getByLabelText('Account');
  await expect.element(account).toHaveAttribute('inputmode', 'numeric');
  await account.fill('1');
  await expect.element(count).toHaveTextContent('3 of 6 lines');
  await account.fill('');

  await screen.getByLabelText('Email').fill('a@example.com');
  await expect.element(count).toHaveTextContent('3 of 6 lines');
  expect(msgs()).toEqual(['navigating', 'starting account creation', 'account created']);
});

it('shows Clear only while a filter is set, and clears every filter', async () => {
  const { screen, count, clearButton } = await emitRun();
  expect(clearButton.elements()).toHaveLength(0);
  await screen.getByText('More filters').click();

  await screen.getByLabelText('Level').selectOptions('INFO');
  await expect.element(clearButton).toBeVisible();
  await screen.getByLabelText('Text').fill('a');
  await screen.getByLabelText('Component').selectOptions('browser');
  await screen.getByLabelText('Worker').selectOptions('1');
  await screen.getByLabelText('Account').fill('1');
  await screen.getByLabelText('Email').fill('a@example.com');

  await clearButton.click();
  await expect.element(count).toHaveTextContent('6 lines');
  expect(clearButton.elements()).toHaveLength(0);
  const values = ['Level', 'Text', 'Component', 'Worker', 'Account', 'Email'].map((label) =>
    value(screen, label),
  );
  expect(values).toEqual(['', '', '', '', '', '']);
});

it('says when no line matches, and clears the filters from there', async () => {
  const { screen, count, msgs, clearButton } = await emitRun();
  await screen.getByLabelText('Text').fill('no such text');
  await expect.element(count).toHaveTextContent('0 of 6 lines');
  const none = screen.getByText(/^No line matches these filters\./);
  await expect.element(none).toBeVisible();
  expect(screen.container.querySelector('[role="region"]')).toBeNull();
  await expect.element(clearButton).toBeVisible();

  await screen.getByRole('button', { name: 'Clear the filters' }).click();
  await expect.element(count).toHaveTextContent('6 lines');
  expect(msgs()).toEqual(run.map((l) => l.msg));
  expect(none.elements()).toHaveLength(0);
  expect(value(screen, 'Text')).toBe('');
});

it('clears the lines on a line of a new run, and counts one line', async () => {
  const { screen, source, count, msgs } = await emitRun();
  source.emit(line(5, 'INFO', 'run started', { run_id: '5' }));
  await expect.element(count).toHaveTextContent('1 line');
  expect(msgs()).toEqual(['run started']);
  const link = screen.getByRole('link', { name: 'Download the log file' });
  await expect.element(link).toHaveAttribute('href', '/api/runs/5/log');
  await expect
    .element(screen.getByRole('link', { name: 'Run 5', exact: true }))
    .toHaveAttribute('href', '/runs/5');
});

it('counts the lines of a filter in the singular', async () => {
  const { screen, source, count } = await renderPage();
  source.emit(line(6, 'INFO', 'run started'));
  await expect.element(count).toHaveTextContent('1 line');
  await screen.getByLabelText('Level').selectOptions('WARN');
  await expect.element(count).toHaveTextContent('0 of 1 line');
});

it('closes the stream on destroy', async () => {
  const { screen, source } = await renderPage();
  await screen.unmount();
  expect(source.closed).toBe(true);
});
