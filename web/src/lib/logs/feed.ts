import type { components } from '#lib/api.gen.d.ts';

type LogLine = components['schemas']['LogLine'];

/**
 * Holds the lines of the shown run. A line of a new run clears the held
 * lines. The lines of one burst, such as the replay at a connect, reach
 * onLines in one call at the next frame, so the view filters once per frame
 * and not once per line.
 */
export class LogFeed {
  readonly #onLines: (lines: LogLine[]) => void;
  readonly #schedule: (flush: () => void) => void;
  #runId: number | undefined;
  #lines: LogLine[] = []; // mutation-ignore: ArrayDeclaration: the first line always has a new run id, so it replaces this array
  #pending = false;

  /** Passes a copy of the held lines to onLines. schedule runs the flush, by default at the next frame. */
  constructor(
    onLines: (lines: LogLine[]) => void,
    schedule: (flush: () => void) => void = (flush) => requestAnimationFrame(flush),
  ) {
    this.#onLines = onLines;
    this.#schedule = schedule;
  }

  /** Takes one line from the stream. */
  add(line: LogLine): void {
    if (line.run_id !== this.#runId) {
      this.#runId = line.run_id;
      this.#lines = [];
    }
    this.#lines.push(line);
    if (this.#pending) {
      return;
    }
    this.#pending = true;
    this.#schedule(() => {
      this.#pending = false;
      this.#onLines([...this.#lines]);
    });
  }
}
