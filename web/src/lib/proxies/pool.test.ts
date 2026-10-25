import { describe, expect, it } from 'vitest';
import { runLimited } from './pool.ts';

// Records each started item, and lets the test end each call by hand.
function heldWork() {
  const started: number[] = [];
  const ends = new Map<number, () => void>();
  let inFlight = 0;
  let maxInFlight = 0;
  const work = (item: number) =>
    new Promise<void>((done) => {
      started.push(item);
      inFlight += 1;
      maxInFlight = Math.max(maxInFlight, inFlight);
      ends.set(item, () => {
        inFlight -= 1;
        done();
      });
    });
  const end = (item: number) => {
    const fn = ends.get(item);
    if (fn === undefined) throw new Error(`item ${String(item)} did not start`);
    fn();
  };
  return { started, end, work, max: () => maxInFlight };
}

// Lets each pending promise callback run.
function settle(): Promise<void> {
  return new Promise((done) => setTimeout(done, 0));
}

describe('runLimited', () => {
  it('starts at most limit calls, and starts the next item when a call ends', async () => {
    const held = heldWork();
    let finished = false;
    const all = runLimited([0, 1, 2, 3, 4, 5], 4, held.work).then(() => {
      finished = true;
    });
    await settle();
    expect(held.started).toEqual([0, 1, 2, 3]);

    held.end(2);
    await settle();
    expect(held.started).toEqual([0, 1, 2, 3, 4]);

    for (const item of [0, 1, 3, 4]) {
      held.end(item);
    }
    await settle();
    expect(held.started).toEqual([0, 1, 2, 3, 4, 5]);
    expect(finished).toBe(false);

    held.end(5);
    await all;
    expect(held.max()).toBe(4);
  });

  it('starts every item at once under a limit above the count', async () => {
    const held = heldWork();
    const all = runLimited([7, 8], 4, held.work);
    await settle();
    expect(held.started).toEqual([7, 8]);
    held.end(7);
    held.end(8);
    await all;
  });

  it('runs one call at a time under a limit of 1', async () => {
    const held = heldWork();
    const all = runLimited([1, 2], 1, held.work);
    await settle();
    expect(held.started).toEqual([1]);
    held.end(1);
    await settle();
    expect(held.started).toEqual([1, 2]);
    held.end(2);
    await all;
  });

  it('resolves at once with no item', async () => {
    const held = heldWork();
    await runLimited([], 4, held.work);
    expect(held.started).toEqual([]);
  });
});
