<script lang="ts">
  import { resolve } from '$app/paths';

  interface Props {
    /** The path of the page on screen. Its link and the links of its sub-pages get aria-current. */
    pathname: string;
  }

  const { pathname }: Props = $props();

  const links = [
    { label: 'Run', href: resolve('/') }, // mutation-ignore: StringLiteral: '' also resolves to /
    { label: 'Logs', href: resolve('/logs') },
    { label: 'Runs', href: resolve('/runs') },
    { label: 'Accounts', href: resolve('/accounts') },
    { label: 'Proxies', href: resolve('/proxies') },
    { label: 'Checks', href: resolve('/checks') },
  ];

  // A detail page such as /runs/5 marks the link of its list.
  function current(href: string): 'page' | undefined {
    return pathname === href || pathname.startsWith(`${href}/`) ? 'page' : undefined;
  }
</script>

<nav aria-label="Main">
  <ul class="flex flex-wrap gap-1 text-sm font-medium">
    {#each links as link (link.href)}
      <li>
        <a
          href={link.href}
          aria-current={current(link.href)}
          class="block rounded-md px-2.5 py-1.5 text-muted hover:bg-panel hover:text-ink aria-[current=page]:bg-panel aria-[current=page]:text-ink aria-[current=page]:shadow-[inset_0_-2px_0_var(--color-accent)]"
          >{link.label}</a
        >
      </li>
    {/each}
  </ul>
</nav>
