/** The callbacks of one server-sent event stream. */
export interface StreamHandlers<T> {
  /** Receives the JSON value of each message. */
  onMessage: (value: T) => void;
  /** Receives true on a stream error, and false on each message. The browser reconnects on its own. */
  onReconnecting: (reconnecting: boolean) => void;
}

/**
 * Opens the server-sent event stream at url. Each message must hold one JSON
 * value of type T. The returned function closes the stream.
 */
export function subscribe<T>(url: string, handlers: StreamHandlers<T>): () => void {
  const source = new EventSource(url);
  source.onmessage = (e: MessageEvent<string>) => {
    handlers.onReconnecting(false);
    handlers.onMessage(JSON.parse(e.data) as T);
  };
  source.onerror = () => {
    handlers.onReconnecting(true);
  };
  return () => {
    source.close();
  };
}
