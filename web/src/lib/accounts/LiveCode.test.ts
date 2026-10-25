import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import LiveCode from './LiveCode.svelte';

afterEach(() => {
  vi.useRealTimers();
});

async function renderCode(seconds: number) {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  const onExpire = vi.fn();
  const screen = await render(LiveCode, { code: '123456', seconds, onExpire });
  return { screen, onExpire };
}

describe('LiveCode', () => {
  it('shows the code and counts down the seconds of its window', async () => {
    const { screen, onExpire } = await renderCode(3);
    await expect.element(screen.getByText('123456', { exact: true })).toBeVisible();
    await expect.element(screen.getByText('(3 s left)', { exact: true })).toBeVisible();
    vi.advanceTimersByTime(1000);
    await expect.element(screen.getByText('(2 s left)', { exact: true })).toBeVisible();
    vi.advanceTimersByTime(1000);
    await expect.element(screen.getByText('(1 s left)', { exact: true })).toBeVisible();
    expect(onExpire).not.toHaveBeenCalled();
  });

  it('calls onExpire once at the end of the window', async () => {
    const { screen, onExpire } = await renderCode(2);
    vi.advanceTimersByTime(1999);
    expect(onExpire).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(onExpire).toHaveBeenCalledOnce();
    await expect.element(screen.getByText('(0 s left)', { exact: true })).toBeVisible();
    vi.advanceTimersByTime(5000);
    expect(onExpire).toHaveBeenCalledOnce();
  });

  it('starts a new countdown when the seconds change', async () => {
    const { screen, onExpire } = await renderCode(1);
    vi.advanceTimersByTime(1000);
    expect(onExpire).toHaveBeenCalledOnce();
    await screen.rerender({ code: '654321', seconds: 30 });
    await expect.element(screen.getByText('654321', { exact: true })).toBeVisible();
    await expect.element(screen.getByText('(30 s left)', { exact: true })).toBeVisible();
    vi.advanceTimersByTime(29_000);
    expect(onExpire).toHaveBeenCalledOnce();
    vi.advanceTimersByTime(1000);
    expect(onExpire).toHaveBeenCalledTimes(2);
  });

  it('stops its tick when it unmounts', async () => {
    const { screen, onExpire } = await renderCode(1);
    await screen.unmount();
    vi.advanceTimersByTime(5000);
    expect(onExpire).not.toHaveBeenCalled();
  });
});
