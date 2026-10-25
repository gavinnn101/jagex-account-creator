import type { components } from '#lib/api.gen.d.ts';

type LogLine = components['schemas']['LogLine'];

/** The filters of the log view. An empty string selects every line. */
export interface LogFilter {
  /** The lowest level to show: DEBUG, INFO, WARN or ERROR. */
  level: string;
  /** A text in the message or in an attribute value, in any case. */
  text: string;
  /** The value of the component attribute. */
  component: string;
  /** The value of the worker attribute. */
  worker: string;
  /** The value of the account attribute. */
  account: string;
  /** The email of a record line. It selects every line of that attempt. */
  email: string;
}

/** The filter that selects every line. */
export const noFilter: LogFilter = {
  level: '',
  text: '',
  component: '',
  worker: '',
  account: '',
  email: '',
};

/** The levels of the level filter, lowest first. */
export const levels = ['DEBUG', 'INFO', 'WARN', 'ERROR'];

type LevelName = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';

const levelBase: Record<LevelName, number> = { DEBUG: -4, INFO: 0, WARN: 4, ERROR: 8 };

// The number of a slog level text, such as INFO or WARN+2. Any other text gives undefined.
function levelValue(level: string): number | undefined {
  const match = /^(DEBUG|INFO|WARN|ERROR)([+-]\d+)?$/.exec(level);
  if (match === null) {
    return undefined;
  }
  const [, name, offset] = match;
  return levelBase[name as LevelName] + Number(offset ?? 0);
}

function attrValue(line: LogLine, key: string): string | undefined {
  return line.attrs.find((a) => a.key === key)?.value;
}

// The record line ends an attempt. Only it carries the email at INFO.
function isRecordLine(line: LogLine): boolean {
  return line.msg === 'account created' || line.msg === 'account creation failed';
}

const numeric = new Intl.Collator(undefined, { numeric: true });

/** Returns each value of the attribute key in lines once, in numeric order. */
export function attrValues(lines: readonly LogLine[], key: string): string[] {
  const values = new Set<string>();
  for (const line of lines) {
    const value = attrValue(line, key);
    if (value !== undefined) {
      values.add(value);
    }
  }
  return [...values].sort(numeric.compare);
}

// A retry keeps its worker and its account number, so the key also holds the earlier tries.
function attemptKey(line: LogLine): string | undefined {
  const worker = attrValue(line, 'worker');
  const account = attrValue(line, 'account');
  return worker === undefined || account === undefined ? undefined : `${worker}/${account}`;
}

// The attempts of the record lines that carry email. The set never holds undefined.
function attemptsOf(lines: readonly LogLine[], email: string): ReadonlySet<string | undefined> {
  const keys = new Set<string>();
  for (const line of lines) {
    const key = attemptKey(line);
    if (key !== undefined && isRecordLine(line) && attrValue(line, 'email') === email) {
      keys.add(key);
    }
  }
  return keys;
}

type Test = (line: LogLine) => boolean;

// A line with no level passes, so a line that did not parse stays in view.
function levelTest(level: string): Test {
  const min = levelValue(level) ?? -Infinity;
  return (line) => (levelValue(line.level) ?? Infinity) >= min;
}

function textTest(text: string): Test {
  const needle = text.toLowerCase();
  const has = (value: string) => value.toLowerCase().includes(needle);
  return (line) => has(line.msg) || line.attrs.some((a) => has(a.value));
}

function attrTest(key: string, value: string): Test {
  return (line) => attrValue(line, key) === value;
}

function emailTest(lines: readonly LogLine[], email: string): Test {
  const attempts = attemptsOf(lines, email);
  return (line) => attempts.has(attemptKey(line));
}

/**
 * Returns the lines that pass each set filter, in their order. The email
 * filter selects each line with the worker and the account of a record line
 * that carries the email.
 */
export function filterLines(lines: readonly LogLine[], filter: LogFilter): LogLine[] {
  const tests: Test[] = [];
  if (filter.level !== '') tests.push(levelTest(filter.level)); // mutation-ignore: ConditionalExpression, StringLiteral: an empty level passes every line
  if (filter.text !== '') tests.push(textTest(filter.text)); // mutation-ignore: ConditionalExpression, StringLiteral: an empty text passes every line
  if (filter.component !== '') tests.push(attrTest('component', filter.component));
  if (filter.worker !== '') tests.push(attrTest('worker', filter.worker));
  if (filter.account !== '') tests.push(attrTest('account', filter.account));
  if (filter.email !== '') tests.push(emailTest(lines, filter.email));
  return lines.filter((line) => tests.every((test) => test(line)));
}
