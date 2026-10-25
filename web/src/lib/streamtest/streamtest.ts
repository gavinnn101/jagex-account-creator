// The test helpers of the server-sent event streams. Only test files import this module.
import { vi } from 'vitest';
import type { components } from '#lib/api.gen.d.ts';

type RunState = components['schemas']['RunState'];

/** A fake EventSource. The test sends each message and each error by hand. */
export class FakeEventSource {
  /** The sources made since the last stubEventSource. */
  static count = 0;
  static last: FakeEventSource | undefined;
  onmessage: ((e: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(readonly url: string) {
    FakeEventSource.count += 1;
    FakeEventSource.last = this;
  }
  close(): void {
    this.closed = true;
  }
  /** Sends one message with data as its text. */
  send(data: string): void {
    this.onmessage?.(new MessageEvent('message', { data }));
  }
  /** Sends value as one JSON message. */
  emit(value: object): void {
    this.send(JSON.stringify(value));
  }
  fail(): void {
    this.onerror?.();
  }
}

/** Makes FakeEventSource the global EventSource and clears its count. vi.unstubAllGlobals restores the real one. */
export function stubEventSource(): void {
  vi.stubGlobal('EventSource', FakeEventSource);
  FakeEventSource.count = 0;
  FakeEventSource.last = undefined;
}

/** Returns the source that the code under test made last. It throws when the code made none. */
export function lastSource(): FakeEventSource {
  const source = FakeEventSource.last;
  if (source === undefined) throw new Error('no EventSource');
  return source;
}

/** Returns a run state in phase with no worker, no cooldown and zero counts, then the fields of over. */
export function runState(phase: RunState['phase'], over: Partial<RunState> = {}): RunState {
  return {
    phase,
    workers: [],
    cooldowns: [],
    counts: {
      attempts: 0,
      accounts: 0,
      unconfirmed: 0,
      failures: {},
      browser_bytes_sent: 0,
      browser_bytes_received: 0,
      http_bytes_sent: 0,
      http_bytes_received: 0,
    },
    ...over,
  };
}
