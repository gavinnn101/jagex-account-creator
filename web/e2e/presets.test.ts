import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test';
import type { components } from '#lib/api.gen.d.ts';
import { expectNoAxeViolations, openSections, readJSON } from './helpers.ts';

type Config = components['schemas']['Config'];
type Preset = components['schemas']['Preset'];
type PresetList = components['schemas']['PresetList'];

// Only this file writes presets. The name is its own, so a run of the other files never meets it.
const name = 'e2e presets file';

// The values that make a valid config. Each save sets them, because the other files change the config of serve.
const valid: Record<string, string> = {
  'IMAP host': 'imap.presets.example',
  'IMAP port': '993',
  'IMAP user': 'e2e-presets',
  'IMAP domains': 'presets.example\nmore.presets.example',
  'Max browsers': '2',
};

async function listed(request: APIRequestContext): Promise<Preset[]> {
  return (await readJSON<PresetList>(request, '/api/presets')).presets;
}

// The one preset of this file in presets.
function ours(presets: Preset[]): Preset {
  const found = presets.filter((p) => p.name === name);
  expect(found).toHaveLength(1);
  const [preset] = found;
  if (preset === undefined) throw new Error(`no preset ${name}`);
  return preset;
}

// The values that the form shows for config, keyed by the label of each control.
function fieldsOf(config: Config): Record<string, string | boolean> {
  return {
    Debug: config.debug,
    Database: config.database,
    'Direct, with no proxy': config.proxy.direct,
    Headless: config.chrome.headless,
    'Chrome timeout': config.chrome.timeout,
    'IMAP host': config.email.imap_host,
    'IMAP port': String(config.email.imap_port),
    'IMAP user': config.email.imap_user,
    'IMAP password': '',
    'IMAP domains': config.email.imap_domains.join('\n'),
    'Password length': String(config.account.password_length),
    'Enable TOTP': config.account.enable_totp,
    'Account count': String(config.concurrency.account_count),
    'Max workers': String(config.concurrency.max_workers),
    'Max browsers': String(config.concurrency.max_browsers),
    'Account timeout': config.concurrency.account_timeout,
  };
}

// Reads each control of fields as fieldsOf keys it: a checkbox by its state, any other by its value.
async function shownFields(
  form: Locator,
  fields: Record<string, string | boolean>,
): Promise<Record<string, string | boolean>> {
  const entries = await Promise.all(
    Object.entries(fields).map(async ([label, want]): Promise<[string, string | boolean]> => {
      const control = form.getByLabel(label, { exact: true });
      return [
        label,
        typeof want === 'boolean' ? await control.isChecked() : await control.inputValue(),
      ];
    }),
  );
  return Object.fromEntries(entries);
}

// Fills the valid values, maxWorkers and a password, then saves the form under name.
// It returns the answer of the save.
async function saveAs(
  page: Page,
  form: Locator,
  presets: Locator,
  maxWorkers: number,
): Promise<Preset> {
  await openSections(form);
  for (const [label, value] of Object.entries(valid)) {
    await form.getByLabel(label, { exact: true }).fill(value);
  }
  await form.getByLabel('Max workers', { exact: true }).fill(String(maxWorkers));
  // A preset holds no password, and the config of serve can have none.
  await form.getByLabel('IMAP password', { exact: true }).fill('e2e-presets-password');
  await presets.getByLabel('Preset name', { exact: true }).fill(name);
  const answer = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/presets',
  );
  await presets.getByRole('button', { name: 'Save as preset', exact: true }).click();
  const response = await answer;
  expect(response.status()).toBe(200);
  await expect(presets.getByRole('status')).toHaveText('Preset saved.');
  return (await response.json()) as Preset;
}

test('a preset saves, loads, saves again under its name and deletes', async ({ page, request }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Run');
  const form = page.getByRole('form', { name: 'Run config' });
  // The presets sit in the run controls above the status, outside the config form.
  const presets = page.getByRole('group', { name: 'Presets' });
  const select = presets.getByLabel('Preset', { exact: true });
  const options = () => select.locator('option').allTextContents();

  const saved = await test.step('save, then find the preset in the select', async () => {
    const answer = await saveAs(page, form, presets, 3);
    const list = await listed(request);
    expect(ours(list)).toEqual(answer);
    await expect.poll(options).toEqual(list.map((p) => p.name));
    await expect(select).toHaveValue(String(answer.id));
    await expectNoAxeViolations(page);
    return answer;
  });

  await test.step('load the preset over edited fields', async () => {
    await openSections(form);
    await form.getByLabel('Max workers', { exact: true }).fill('6');
    await form.getByLabel('IMAP host', { exact: true }).fill('edited.example');
    await form.getByLabel('IMAP password', { exact: true }).fill('typed-password');
    await presets.getByLabel('Preset name', { exact: true }).fill('');
    await select.selectOption({ label: name });
    await presets.getByRole('button', { name: 'Load', exact: true }).click();
    const want = fieldsOf(ours(await listed(request)).config);
    expect(await shownFields(form, want)).toEqual(want);
    await expect(presets.getByLabel('Preset name', { exact: true })).toHaveValue(name);
    await expectNoAxeViolations(page);
  });

  await test.step('save again under the same name', async () => {
    const again = await saveAs(page, form, presets, 4);
    expect(again.id).toBe(saved.id);
    expect(again.config.concurrency.max_workers).toBe(4);
    expect(ours(await listed(request))).toEqual(again);
    await expectNoAxeViolations(page);
  });

  await test.step('delete the preset with the confirm', async () => {
    await select.selectOption({ label: name });
    await presets.getByRole('button', { name: `Delete ${name}`, exact: true }).click();
    const confirm = presets.getByRole('button', { name: `Confirm delete ${name}`, exact: true });
    await expect(confirm).toBeVisible();
    await expectNoAxeViolations(page);

    const answer = page.waitForResponse(
      (r) =>
        r.request().method() === 'DELETE' &&
        new URL(r.url()).pathname === `/api/presets/${String(saved.id)}`,
    );
    await confirm.click();
    expect((await answer).status()).toBe(204);
    await expect(presets.getByRole('status')).toHaveText('Preset deleted.');
    expect((await listed(request)).filter((p) => p.name === name)).toEqual([]);
    await expect.poll(options).not.toContain(name);
    await expectNoAxeViolations(page);
  });
});
