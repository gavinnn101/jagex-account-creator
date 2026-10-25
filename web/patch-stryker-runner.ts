// Edits the installed Stryker Vitest runner. Each edit must match its bare or its patched text one time,
// so a runner release that changes the text fails the install.
import { readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';

interface Edit {
  file: string;
  what: string;
  pattern: RegExp;
  text: string;
}

const fileCheckMarker = '        // Added by patch-stryker-runner.ts.';
const errorsSetCheck = '        if (!failure && this.ctx.state.errorsSet.size > 0) {';
const fileCheck = `${fileCheckMarker} Stryker runs a mutant again on a new process when its run throws.
        const failedFiles = this.ctx.state.getFiles().filter((file) => file.result?.state === 'fail');
        if (!failure && failedFiles.length > 0) {
            const fileErrors = failedFiles.map((file) => {
                const error = file.result.errors?.[0];
                const cause = error?.cause?.message ? \` Cause: \${error.cause.message}\` : '';
                return \`\${file.filepath}: \${error?.message ?? 'no error message'}\${cause}\`;
            });
            throw new Error(\`Test files failed outside their tests:\\n\${fileErrors.join('\\n')}\`);
        }
`;

const edits: Edit[] = [
  // Vitest 5 matches a test name filter against the suite chain joined with ' > '. The runner joins it with a space.
  ...['test-helpers.js', 'stryker-setup.js'].map((file) => ({
    file,
    what: 'the test name join',
    pattern: /nameParts\.join\(' (?:> )?'\)/g,
    text: "nameParts.join(' > ')",
  })),
  // The runner reads only the test results, so a test file that fails to import runs no test and its mutants survive.
  // The optional group takes an earlier version of the check, so a second run replaces it.
  {
    file: 'vitest-test-runner.js',
    what: 'the errorsSet check',
    pattern: new RegExp(
      `(?:${RegExp.escape(fileCheckMarker)}[\\s\\S]*?)?${RegExp.escape(errorsSetCheck)}`,
      'g',
    ),
    text: fileCheck + errorsSetCheck,
  },
];

const runner = dirname(
  createRequire(import.meta.url).resolve('@stryker-mutator/vitest-runner/package.json'),
);
// Every edit passes its check before the script writes a file.
const texts = new Map<string, string>();
for (const edit of edits) {
  const file = join(runner, 'dist', 'src', edit.file);
  const text = texts.get(file) ?? readFileSync(file, 'utf8');
  const count = text.match(edit.pattern)?.length ?? 0;
  if (count !== 1) {
    throw new Error(
      `${file} holds ${edit.what} ${String(count)} times, not 1. See "Front end (web/)" in CLAUDE.md.`,
    );
  }
  texts.set(
    file,
    text.replace(edit.pattern, () => edit.text),
  );
}
for (const [file, text] of texts) {
  writeFileSync(file, text);
}
