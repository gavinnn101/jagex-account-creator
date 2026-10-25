/**
 * Calls work on each item in order, with at most limit calls in flight. A call
 * that ends frees its slot for the next item. The promise resolves when every
 * call has resolved. work must not reject: a rejection stops the slot and
 * rejects the promise.
 */
export async function runLimited<T>(
  items: readonly T[],
  limit: number,
  work: (item: T) => Promise<void>,
): Promise<void> {
  // The slots share one iterator, so each item goes to exactly one slot.
  const queue = items.values();
  async function slot(): Promise<void> {
    for (const item of queue) {
      await work(item);
    }
  }
  await Promise.all(Array.from({ length: limit }, slot));
}
