import { getContext, setContext } from 'svelte';
import type { components } from '#lib/api.gen.d.ts';
import { subscribe, type StreamHandlers } from '#lib/events.ts';

type RunState = components['schemas']['RunState'];

/**
 * The one run state stream of the app. The layout opens it, and each screen
 * subscribes. A new subscriber first gets the reconnecting flag and the last
 * state, as a new stream of its own would give them.
 */
export class RunStream {
  #last: RunState | undefined;
  #reconnecting = false;
  readonly #subscribers = new Set<StreamHandlers<RunState>>();

  /** Opens the server-sent event stream of the run state. The returned function closes it. */
  open(): () => void {
    return subscribe<RunState>('/api/events', {
      onMessage: (state) => {
        this.#last = state;
        for (const s of this.#subscribers) s.onMessage(state);
      },
      onReconnecting: (value) => {
        this.#reconnecting = value;
        for (const s of this.#subscribers) s.onReconnecting(value);
      },
    });
  }

  /** Adds handlers and replays the flag and the last state to them. The returned function removes them. */
  subscribe(handlers: StreamHandlers<RunState>): () => void {
    this.#subscribers.add(handlers);
    handlers.onReconnecting(this.#reconnecting);
    if (this.#last !== undefined) {
      handlers.onMessage(this.#last);
    }
    return () => {
      this.#subscribers.delete(handlers);
    };
  }
}

/** The context key of the run stream. A component test passes its stream under this key. */
export const runStreamKey = Symbol();

/** Gives stream to the components below the caller. Call it at component start, before a child subscribes. */
export function provideRunStream(stream: RunStream): void {
  setContext(runStreamKey, stream);
}

/** Returns the run stream of the layout. Call it at component start. */
export function runStream(): RunStream {
  return getContext<RunStream>(runStreamKey);
}
