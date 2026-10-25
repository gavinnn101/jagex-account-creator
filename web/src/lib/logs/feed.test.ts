import { expect, it } from 'vitest';
import type { components } from '#lib/api.gen.d.ts';
import { LogFeed } from './feed.ts';

type LogLine = components['schemas']['LogLine'];

function line(runId: number, msg: string): LogLine {
  return { run_id: runId, time: '', level: 'INFO', msg, attrs: [] };
}

function feed() {
  const calls: LogLine[][] = [];
  const queued: (() => void)[] = [];
  const f = new LogFeed(
    (lines) => calls.push(lines),
    (flush) => queued.push(flush),
  );
  const flush = () => {
    for (const run of queued.splice(0)) {
      run();
    }
  };
  const msgs = () => calls.map((lines) => lines.map((l) => l.msg));
  return { f, calls, queued, flush, msgs };
}

it('passes the lines of one burst in one call', () => {
  const { f, queued, flush, msgs } = feed();
  f.add(line(1, 'a'));
  f.add(line(1, 'b'));
  expect(queued).toHaveLength(1);
  expect(msgs()).toEqual([]);
  flush();
  expect(msgs()).toEqual([['a', 'b']]);
});

it('passes a new copy of the held lines on each flush', () => {
  const { f, calls, queued, flush, msgs } = feed();
  f.add(line(1, 'a'));
  flush();
  f.add(line(1, 'b'));
  expect(queued).toHaveLength(1);
  flush();
  expect(msgs()).toEqual([['a'], ['a', 'b']]);
  expect(calls[0]).not.toBe(calls[1]);
});

it('clears the held lines on a line of a new run', () => {
  const { f, flush, msgs } = feed();
  f.add(line(1, 'a'));
  f.add(line(2, 'run started'));
  f.add(line(2, 'b'));
  flush();
  expect(msgs()).toEqual([['run started', 'b']]);
});

it('flushes at the next frame by default', async () => {
  const calls: LogLine[][] = [];
  const f = new LogFeed((lines) => calls.push(lines));
  f.add(line(1, 'a'));
  expect(calls).toEqual([]);
  await new Promise((resolve) => requestAnimationFrame(resolve));
  expect(calls).toEqual([[line(1, 'a')]]);
});
