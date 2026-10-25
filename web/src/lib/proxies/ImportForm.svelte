<script lang="ts">
  import { apiClient } from '#lib/api.ts';
  import type { components } from '#lib/api.gen.d.ts';
  import Button from '#lib/ui/Button.svelte';
  import Field from '#lib/ui/Field.svelte';
  import TextArea from '#lib/ui/TextArea.svelte';

  type ProxyImport = components['schemas']['ProxyImport'];

  interface Props {
    /** Runs after each import that the server accepted. */
    onImported: () => Promise<void>;
  }

  const { onImported }: Props = $props();

  let text = $state('');
  let importing = $state(false);
  let counts: ProxyImport | undefined = $state();
  let problem = $state('');

  // The file only fills the text area, so the operator sees the lines before the import.
  async function readFile(e: Event & { currentTarget: HTMLInputElement }): Promise<void> {
    const file = e.currentTarget.files?.[0]; // mutation-ignore: OptionalChaining: a file input always has a FileList
    if (file !== undefined) {
      text = await file.text();
    }
  }

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    importing = true;
    counts = undefined;
    problem = '';
    try {
      // The body is the text of a proxy file, so it goes as it is.
      const { data, error } = await apiClient(fetch).POST('/api/proxies/import', {
        body: text,
        headers: { 'Content-Type': 'text/plain' },
        bodySerializer: (body: string) => body,
      });
      if (error === undefined) {
        counts = data;
        await onImported();
      } else {
        problem = error.message;
      }
    } catch (err) {
      problem = String(err);
    }
    importing = false;
  }
</script>

<form aria-label="Import proxies" class="flex flex-col gap-gap" onsubmit={submit}>
  <TextArea
    label="Proxy lines"
    hint="One proxy per line: [scheme://]host:port[:user:pass]. The scheme is http, socks5 or socks5h."
    rows={6}
    spellcheck={false}
    autocomplete="off"
    bind:value={text}
  />
  <Field
    label="Proxy file"
    type="file"
    accept=".txt,text/plain"
    hint="The file fills the text area."
    onchange={readFile}
  />
  <div>
    <Button variant="primary" type="submit" disabled={importing}>Import</Button>
  </div>
  {#if counts !== undefined}
    <p role="status">
      Added {counts.added}, updated {counts.updated}, repeated {counts.repeated}.
    </p>
  {/if}
  {#if problem !== ''}
    <p role="alert" class="text-danger">Import failed: {problem}</p>
  {/if}
</form>
