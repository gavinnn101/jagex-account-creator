/** The text of a number whose divisor is 0. */
export const dash = '—';

const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'];

/** Returns n out of d, or undefined at a d of 0. */
export function ratio(n: number, d: number): number | undefined {
  return d === 0 ? undefined : n / d;
}

/**
 * Returns n bytes in binary units with one decimal, for example 530.5KiB. A
 * count below 1024 has no decimal, for example 512B. The log lines use the same form.
 */
export function formatBytes(n: number): string {
  if (n < 1024) {
    return `${String(n)}B`;
  }
  let exp = Math.floor(Math.log2(n) / 10);
  let tenths = Math.round((n / 1024 ** exp) * 10);
  // A value that rounds to 1024.0 is 1.0 in the next unit.
  if (tenths >= 10240) {
    exp += 1;
    tenths = 10;
  }
  return `${(tenths / 10).toFixed(1)}${String(units[exp - 1])}`;
}

/** Returns part out of whole as a percent with one decimal, for example 66.7%. It is a dash at a whole of 0. */
export function percent(part: number, whole: number): string {
  const rate = ratio(part, whole);
  return rate === undefined ? dash : `${(rate * 100).toFixed(1)}%`;
}

/** Returns whole seconds as m:ss, or as h:mm:ss from one hour. A negative count gives 0:00. */
export function formatSeconds(count: number): string {
  const total = Math.max(0, count);
  const seconds = String(total % 60).padStart(2, '0');
  const minutes = Math.floor(total / 60) % 60;
  const hours = Math.floor(total / 3600);
  if (hours === 0) {
    return `${String(minutes)}:${seconds}`;
  }
  return `${String(hours)}:${String(minutes).padStart(2, '0')}:${seconds}`;
}

/**
 * Returns the time from since to now as m:ss, or as h:mm:ss from one hour. A
 * since after now gives 0:00. The time in step is elapsedSince(attempt.step_started_at, now).
 */
export function elapsedSince(since: string, now: Date): string {
  return formatSeconds(Math.floor((now.getTime() - Date.parse(since)) / 1000));
}

/**
 * Returns the time from now to until in the form of elapsedSince. A part of a
 * second counts as a whole second, so only an until that is not after now gives 0:00.
 */
export function remainingUntil(until: string, now: Date): string {
  return formatSeconds(Math.ceil((Date.parse(until) - now.getTime()) / 1000));
}

const minuteMs = 60_000;
const hourMs = 60 * minuteMs;
const dayMs = 24 * hourMs;
const monthMs = 30 * dayMs;
const yearMs = 365 * dayMs;

// The screens are in English, so each time text is in English too.
// Each call builds its formatter, so a bad option fails the call and not the module import.
function ago(span: number, unitMs: number, unit: Intl.RelativeTimeFormatUnit): string {
  const relative = new Intl.RelativeTimeFormat('en-US', { style: 'narrow', numeric: 'always' });
  return relative.format(-Math.floor(span / unitMs), unit);
}

/**
 * Returns the time from iso to now in the narrow form, for example "5m ago".
 * The cuts: "now" below 60 s and for a time after now, minutes below 60 min,
 * hours below 24 h, days below 30 d, months of 30 d below 365 d, then years of 365 d.
 */
export function relativeTime(iso: string, now: Date): string {
  const span = now.getTime() - Date.parse(iso);
  if (span < minuteMs) {
    return 'now';
  }
  if (span < hourMs) {
    return ago(span, minuteMs, 'minute');
  }
  if (span < dayMs) {
    return ago(span, hourMs, 'hour');
  }
  if (span < monthMs) {
    return ago(span, dayMs, 'day');
  }
  if (span < yearMs) {
    return ago(span, monthMs, 'month');
  }
  return ago(span, yearMs, 'year');
}

/** Returns iso as the full local date and time with the zone offset, for example "Oct 20, 2026, 2:33:11 PM GMT+5:30". */
export function fullTime(iso: string): string {
  const full = new Intl.DateTimeFormat('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    second: '2-digit',
    timeZoneName: 'shortOffset',
  });
  return full.format(new Date(iso));
}

/** Returns the clock part of a time text, for example "16:47:11.417" of "2026-10-24T16:47:11.417+05:30". Any other text comes back as it is. */
export function clockTime(text: string): string {
  return /T(\d{2}:\d{2}:\d{2}(?:\.\d+)?)/.exec(text)?.[1] ?? text;
}

/** Returns the bytes of one phase, for example "1.0KiB sent, 530.5KiB received". An absent count is 0. */
export function formatTraffic(sent: number | undefined, received: number | undefined): string {
  return `${formatBytes(sent ?? 0)} sent, ${formatBytes(received ?? 0)} received`;
}

function local(date: Date, options: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat('en-US', options).format(date);
}

/**
 * Returns the label of a time tick on a 12-hour clock. As in the default format of
 * d3, the finest local unit that is not 0 picks the form: ":15", "4:25 PM", "4 PM",
 * "Wed 21", "Oct 18" on a Sunday, "October", then "2026".
 */
export function tickTime(date: Date): string {
  if (date.getSeconds() !== 0) {
    return `:${String(date.getSeconds()).padStart(2, '0')}`;
  }
  if (date.getMinutes() !== 0) {
    return local(date, { hour: 'numeric', minute: '2-digit' });
  }
  if (date.getHours() !== 0) {
    return local(date, { hour: 'numeric' });
  }
  if (date.getDate() !== 1) {
    // A week tick falls on a Sunday.
    if (date.getDay() === 0) {
      return local(date, { month: 'short', day: 'numeric' });
    }
    return `${local(date, { weekday: 'short' })} ${String(date.getDate())}`;
  }
  if (date.getMonth() !== 0) {
    return local(date, { month: 'long' });
  }
  return String(date.getFullYear());
}
