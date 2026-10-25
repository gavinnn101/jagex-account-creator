import { describe, expect, it } from 'vitest';
import type { components } from '#lib/api.gen.d.ts';
import { attrValues, filterLines, levels, noFilter, type LogFilter } from './filters.ts';

type LogLine = components['schemas']['LogLine'];

function line(msg: string, level: string, attrs: Record<string, string> = {}): LogLine {
  return {
    run_id: 1,
    time: '2026-10-21T10:00:00.000+02:00',
    level,
    msg,
    attrs: Object.entries(attrs).map(([key, value]) => ({ key, value })),
  };
}

function msgs(lines: LogLine[], filter: Partial<LogFilter>): string[] {
  return filterLines(lines, { ...noFilter, ...filter }).map((l) => l.msg);
}

describe('filterLines', () => {
  it('keeps every line with no filter', () => {
    const lines = [line('a', 'INFO'), line('b', 'DEBUG', { component: 'browser' })];
    expect(msgs(lines, {})).toEqual(['a', 'b']);
  });

  it('keeps the lines at or above the level, and each line with no level', () => {
    const lines = [
      line('debug', 'DEBUG'),
      line('below debug', 'DEBUG-4'),
      line('debug+10', 'DEBUG+10'),
      line('info', 'INFO'),
      line('info+1', 'INFO+1'),
      line('warn', 'WARN'),
      line('warn+10', 'WARN+10'),
      line('error', 'ERROR'),
      line('unparsed', ''),
      line('lower case', 'debug'),
      line('prefixed', 'xDEBUG'),
      line('suffixed', 'DEBUGx'),
      line('bad offset', 'DEBUG+2x'),
    ];
    const unparsed = ['unparsed', 'lower case', 'prefixed', 'suffixed', 'bad offset'];
    expect(msgs(lines, { level: 'INFO' })).toEqual([
      'debug+10',
      'info',
      'info+1',
      'warn',
      'warn+10',
      'error',
      ...unparsed,
    ]);
    expect(msgs(lines, { level: 'WARN' })).toEqual([
      'debug+10',
      'warn',
      'warn+10',
      'error',
      ...unparsed,
    ]);
    expect(msgs(lines, { level: 'ERROR' })).toEqual(['warn+10', 'error', ...unparsed]);
    expect(msgs(lines, { level: 'DEBUG' })).toEqual(
      lines.map((l) => l.msg).filter((m) => m !== 'below debug'),
    );
  });

  it('keeps every line under a level it does not know', () => {
    const lines = [line('debug', 'DEBUG-8'), line('error', 'ERROR')];
    expect(msgs(lines, { level: 'TRACE' })).toEqual(['debug', 'error']);
  });

  it('finds the text in the message or in any attribute value, in any case', () => {
    const lines = [
      line('Checking the Mailbox', 'INFO'),
      line('other', 'INFO', { a: 'x', error: 'MAILBOX closed' }),
      line('mail', 'INFO', { mailbox: 'key only' }),
    ];
    expect(msgs(lines, { text: 'mailBOX' })).toEqual(['Checking the Mailbox', 'other']);
  });

  it('selects by component, worker and account', () => {
    const lines = [
      line('browser', 'INFO', { worker: '1', account: '2', component: 'browser' }),
      line('attempt', 'INFO', { worker: '1', account: '2' }),
      line('other worker', 'INFO', { worker: '10', account: '2' }),
      line('other account', 'INFO', { worker: '1', account: '3' }),
    ];
    expect(msgs(lines, { component: 'browser' })).toEqual(['browser']);
    expect(msgs(lines, { worker: '1' })).toEqual(['browser', 'attempt', 'other account']);
    expect(msgs(lines, { account: '2' })).toEqual(['browser', 'attempt', 'other worker']);
    expect(msgs(lines, { worker: '1', account: '2' })).toEqual(['browser', 'attempt']);
  });

  it('selects each line of the attempt whose record line carries the email', () => {
    const lines = [
      line('run started', 'INFO'),
      line('start', 'INFO', { worker: '1', account: '5' }),
      line('imap', 'DEBUG', { worker: '1', email: 'a@example.com' }),
      line('other', 'INFO', { worker: '2', account: '6' }),
      line('account creation failed', 'ERROR', {
        worker: '1',
        account: '5',
        email: 'a@example.com',
      }),
      line('account created', 'INFO', { worker: '2', account: '6', email: 'b@example.com' }),
      line('retry', 'INFO', { worker: '1', account: '5' }),
    ];
    expect(msgs(lines, { email: 'a@example.com' })).toEqual([
      'start',
      'account creation failed',
      'retry',
    ]);
    expect(msgs(lines, { email: 'b@example.com' })).toEqual(['other', 'account created']);
    expect(msgs(lines, { email: 'A@example.com' })).toEqual([]);
  });

  it('resolves no attempt from a line that is not a record line', () => {
    const lines = [
      line('submitting', 'DEBUG', { worker: '1', account: '5', email: 'a@example.com' }),
      line('start', 'INFO', { worker: '1', account: '5' }),
    ];
    expect(msgs(lines, { email: 'a@example.com' })).toEqual([]);
  });

  it('resolves no attempt from a record line with no worker or no account', () => {
    const lines = [
      line('account created', 'INFO', { worker: '1', email: 'a@example.com' }),
      line('account created', 'INFO', { account: '5', email: 'a@example.com' }),
      line('imap', 'INFO', { worker: '1' }),
      line('run ended', 'INFO'),
    ];
    expect(msgs(lines, { email: 'a@example.com' })).toEqual([]);
  });
});

describe('attrValues', () => {
  it('lists each value of the key once in numeric order', () => {
    const lines = [
      line('a', 'INFO', { worker: '10' }),
      line('b', 'INFO', { worker: '9' }),
      line('c', 'INFO'),
      line('d', 'INFO', { worker: '9' }),
      line('e', 'INFO', { worker: '2' }),
    ];
    expect(attrValues(lines, 'worker')).toEqual(['2', '9', '10']);
  });
});

it('lists the levels lowest first', () => {
  expect(levels).toEqual(['DEBUG', 'INFO', 'WARN', 'ERROR']);
});
