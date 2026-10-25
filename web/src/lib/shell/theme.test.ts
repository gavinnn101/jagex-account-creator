import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import appHTML from '../../app.html?raw';
import { applyTheme, currentTheme, themeKey } from './theme.ts';

const root = document.documentElement;

beforeEach(() => {
  delete root.dataset.theme;
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
  delete root.dataset.theme;
  localStorage.clear();
});

// The head script of app.html, run as the page runs it.
function runHeadScript(): void {
  const code = /<script>([\s\S]*?)<\/script>/.exec(appHTML)?.[1];
  if (code === undefined) throw new Error('app.html has no head script');
  const script = document.createElement('script');
  script.textContent = code;
  document.head.append(script);
  script.remove();
}

describe('the head script of app.html', () => {
  it('reads the key of theme.ts', () => {
    const keys = [...appHTML.matchAll(/getItem\('([^']*)'\)/g)].map((m) => m[1]);
    expect(keys).toEqual([themeKey]);
  });

  it('shows a stored light choice', () => {
    localStorage.setItem(themeKey, 'light');
    runHeadScript();
    expect(root.dataset.theme).toBe('light');
  });

  it.for(['dark', 'Light', ''])('keeps dark for the stored value %j', (stored) => {
    localStorage.setItem(themeKey, stored);
    runHeadScript();
    expect(root.dataset.theme).toBeUndefined();
  });

  it('keeps dark with no stored choice', () => {
    runHeadScript();
    expect(root.dataset.theme).toBeUndefined();
  });

  it('keeps dark when storage is blocked', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    const errors: unknown[] = [];
    const listen = (e: ErrorEvent) => errors.push(e.error);
    window.addEventListener('error', listen);
    try {
      runHeadScript();
    } finally {
      window.removeEventListener('error', listen);
    }
    expect(errors).toEqual([]);
    expect(root.dataset.theme).toBeUndefined();
  });
});

describe('currentTheme', () => {
  it.for([
    [undefined, 'dark'],
    ['dark', 'dark'],
    ['light', 'light'],
    ['other', 'dark'],
  ] as const)('reads the attribute %j as %s', ([attribute, theme]) => {
    const el = document.createElement('html');
    if (attribute !== undefined) el.dataset.theme = attribute;
    expect(currentTheme(el)).toBe(theme);
  });

  it('reads the root of the document by default', () => {
    root.dataset.theme = 'light';
    expect(currentTheme()).toBe('light');
  });
});

describe('applyTheme', () => {
  it.for(['light', 'dark'] as const)('shows and stores %s', (theme) => {
    const el = document.createElement('html');
    applyTheme(theme, el);
    expect(el.dataset.theme).toBe(theme);
    expect(localStorage.getItem(themeKey)).toBe(theme);
  });

  it('sets the root of the document by default', () => {
    applyTheme('light');
    expect(root.dataset.theme).toBe('light');
  });

  it('still shows the theme when storage is blocked', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'QuotaExceededError');
    });
    applyTheme('light');
    expect(root.dataset.theme).toBe('light');
    expect(localStorage.getItem(themeKey)).toBeNull();
  });
});
