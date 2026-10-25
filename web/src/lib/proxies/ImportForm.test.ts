import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import ImportForm from './ImportForm.svelte';

interface Sent {
  method: string;
  path: string;
  type: string | null;
  body: string;
}

// The window sees each submit after the form. Its own preventDefault keeps the test page,
// so a form that lets the submit through fails one test and never reloads the runner.
let prevented: boolean[] = [];
function guard(e: Event): void {
  prevented.push(e.defaultPrevented);
  e.preventDefault();
}

beforeEach(() => {
  prevented = [];
  window.addEventListener('submit', guard);
});

afterEach(() => {
  window.removeEventListener('submit', guard);
  vi.unstubAllGlobals();
});

// Answers each request with the reply, and records each request.
function stubFetch(reply: () => Promise<Response>) {
  const sent: Sent[] = [];
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    const request = new Request(input);
    sent.push({
      method: request.method,
      path: new URL(request.url).pathname,
      type: request.headers.get('content-type'),
      body: await request.text(),
    });
    return reply();
  });
  return sent;
}

async function renderForm() {
  const onImported = vi.fn(() => Promise.resolve());
  const screen = await render(ImportForm, { onImported });
  return {
    screen,
    onImported,
    lines: screen.getByLabelText('Proxy lines'),
    importButton: screen.getByRole('button', { name: 'Import' }),
  };
}

describe('ImportForm', () => {
  it('sends the text as a proxy file and shows the counts', async () => {
    const sent = stubFetch(() =>
      Promise.resolve(Response.json({ added: 2, updated: 1, repeated: 3 })),
    );
    const { screen, onImported, lines, importButton } = await renderForm();
    const area = lines.element();
    expect(area).toHaveValue('');
    expect(area).toHaveAttribute('spellcheck', 'false');
    expect(area).toHaveAttribute('autocomplete', 'off');
    expect(area).toHaveAttribute('rows', '6');
    expect(area).toHaveAccessibleDescription(
      'One proxy per line: [scheme://]host:port[:user:pass]. The scheme is http, socks5 or socks5h.',
    );
    await lines.fill('h.example:1:u:p\nsocks5://s.example:2');
    await importButton.click();
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent('Added 2, updated 1, repeated 3.');
    expect(sent).toEqual([
      {
        method: 'POST',
        path: '/api/proxies/import',
        type: 'text/plain',
        body: 'h.example:1:u:p\nsocks5://s.example:2',
      },
    ]);
    expect(onImported).toHaveBeenCalledOnce();
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });

  it('shows the error of a refused import, and drops the last counts', async () => {
    let refuse = false;
    stubFetch(() =>
      Promise.resolve(
        refuse
          ? Response.json({ message: 'line 2: missing port' }, { status: 400 })
          : Response.json({ added: 1, updated: 0, repeated: 0 }),
      ),
    );
    const { screen, onImported, lines, importButton } = await renderForm();
    await lines.fill('h.example:1');
    await importButton.click();
    await expect.element(screen.getByRole('status')).toBeVisible();
    refuse = true;
    await importButton.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Import failed: line 2: missing port');
    expect(screen.container.querySelector('[role="status"]')).toBeNull();
    expect(onImported).toHaveBeenCalledOnce();
  });

  it('shows a failed request, and clears it on the next import', async () => {
    let fail = true;
    stubFetch(() =>
      fail
        ? Promise.reject(new TypeError('network down'))
        : Promise.resolve(Response.json({ added: 0, updated: 0, repeated: 1 })),
    );
    const { screen, onImported, importButton } = await renderForm();
    await importButton.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Import failed: TypeError: network down');
    expect(onImported).not.toHaveBeenCalled();
    fail = false;
    await importButton.click();
    await expect.element(screen.getByRole('status')).toBeVisible();
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });

  it('keeps the page on a submit', async () => {
    stubFetch(() => Promise.resolve(Response.json({ added: 0, updated: 0, repeated: 0 })));
    const { importButton } = await renderForm();
    await importButton.click();
    // The click dispatches the submit before it returns, so a direct check suffices.
    expect(prevented).toEqual([true]);
  });

  it('turns the button off while the import runs', async () => {
    let answer: (r: Response) => void = () => undefined;
    stubFetch(
      () =>
        new Promise<Response>((done) => {
          answer = done;
        }),
    );
    const { importButton } = await renderForm();
    await importButton.click();
    await expect.element(importButton).toBeDisabled();
    answer(Response.json({ added: 0, updated: 0, repeated: 0 }));
    await expect.element(importButton).not.toBeDisabled();
  });

  it('fills the text area from a chosen file', async () => {
    const { screen, lines } = await renderForm();
    const file = screen.getByLabelText('Proxy file');
    expect(file.element()).toHaveAttribute('type', 'file');
    expect(file.element()).toHaveAccessibleDescription('The file fills the text area.');
    await file.upload(new File(['a.example:1\nb.example:2\n'], 'proxies.txt'));
    await expect.element(lines).toHaveValue('a.example:1\nb.example:2\n');
  });

  it('keeps the text when the file choice is cleared', async () => {
    const { screen, lines } = await renderForm();
    await lines.fill('kept:1');
    const input = screen.getByLabelText('Proxy file').element() as HTMLInputElement;
    input.files = new DataTransfer().files;
    input.dispatchEvent(new Event('change', { bubbles: true }));
    await expect.element(lines).toHaveValue('kept:1');
  });
});
