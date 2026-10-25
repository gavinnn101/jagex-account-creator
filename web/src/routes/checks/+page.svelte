<script lang="ts">
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import { resolve } from '$app/paths';
  import { mailboxDigest } from '#lib/digest.ts';
  import MailboxCheck from '#lib/run/MailboxCheck.svelte';
  import Badge from '#lib/ui/Badge.svelte';
  import type { PageProps } from './$types';

  const { data }: PageProps = $props();
  const check = $derived(data.check);
  const mailbox = $derived(mailboxDigest(data.config));
  // With no user or host, a login can only fail validation, so the check stays off.
  const noMailbox = $derived(mailbox === 'Not set');
  const runHref = resolve('/'); // mutation-ignore: StringLiteral: '' also resolves to /
</script>

<h1 class="mb-section text-2xl font-bold">Checks</h1>

<div class="grid items-start gap-section lg:grid-cols-2">
  <section aria-labelledby="check-chrome" class="flex flex-col gap-gap">
    <h2 id="check-chrome" class="text-lg font-semibold">Chrome</h2>
    <dl class="term-grid items-center gap-x-6 gap-y-2">
      <dt class="font-semibold">Status</dt>
      <dd>
        {#if check.ok}
          <Badge tone="success">Found</Badge>
        {:else}
          <Badge tone="danger">Not found</Badge>
        {/if}
      </dd>
      {#if check.ok}
        <dt class="font-semibold">Version</dt>
        <dd class="flex flex-wrap items-center gap-2">
          {check.version}
          {#if check.mismatch}
            <Badge tone="warning">Differs</Badge>
          {:else}
            <Badge tone="success">Matches</Badge>
          {/if}
        </dd>
      {:else}
        <dt class="font-semibold">Error</dt>
        <dd class="break-words">{check.error}</dd>
      {/if}
      <dt class="font-semibold">Request shape major</dt>
      <dd>{check.shape_major}</dd>
    </dl>
    {#if check.mismatch}
      <p role="alert" class="flex items-start gap-1.5 text-warning">
        <TriangleAlert aria-hidden="true" class="mt-1 size-4 shrink-0" />
        <span>
          The installed Chrome major differs from Chrome {check.shape_major}, the Chrome that the
          request shapes copy. The browser and the HTTP requests then show two different Chrome
          versions.
        </span>
      </p>
    {/if}
  </section>

  <section aria-labelledby="check-mailbox" class="flex flex-col gap-gap">
    <h2 id="check-mailbox" class="text-lg font-semibold">Mailbox</h2>
    <dl class="term-grid gap-x-6 gap-y-2">
      <dt class="font-semibold">Saved mailbox</dt>
      <dd class="break-words">{mailbox}</dd>
    </dl>
    {#if noMailbox}
      <p class="text-muted">
        The saved config has no mailbox. Set it on the
        <a class="text-accent underline" href={runHref}>run screen</a> and save.
      </p>
    {/if}
    <MailboxCheck email={data.config.email} {fetch} disabled={noMailbox} />
  </section>
</div>
