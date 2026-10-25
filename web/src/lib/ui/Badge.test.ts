import { createRawSnippet } from 'svelte';
import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import Badge from './Badge.svelte';

const label = createRawSnippet(() => ({ render: () => '<span>2FA</span>' }));

const tones = ['neutral', 'muted', 'accent', 'success', 'warning', 'danger'] as const;

it.for(tones)('marks the %s tone and shows its children', async (tone) => {
  const screen = await render(Badge, { tone, children: label });
  const badge = screen.getByText('2FA').element().parentElement;
  expect(badge?.dataset.tone).toBe(tone);
});

// The 20 px line of a table cell holds the badge, so a row with a badge is as high as the others.
it('is 20 px high', async () => {
  const screen = await render(Badge, { tone: 'neutral', children: label });
  const badge = screen.getByText('2FA').element().parentElement;
  expect(badge?.getBoundingClientRect().height).toBe(20);
});

// The colors of each tone, as the browser draws them.
it('gives each tone its own look', async () => {
  const shown = [];
  for (const tone of tones) {
    const screen = await render(Badge, { tone, children: label });
    const badge = screen.getByText('2FA').element().parentElement;
    if (badge === null) throw new Error('no badge');
    const style = getComputedStyle(badge);
    shown.push(`${style.backgroundColor} ${style.color} ${style.borderColor}`);
    await screen.unmount();
  }
  expect(new Set(shown).size).toBe(tones.length);
});
