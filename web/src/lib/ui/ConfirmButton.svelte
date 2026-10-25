<script lang="ts">
  import Trash from '@lucide/svelte/icons/trash';
  import X from '@lucide/svelte/icons/x';
  import Button from './Button.svelte';

  interface Props {
    /** The thing that the delete removes, in each button name. */
    name: string;
    /** Runs on the confirm click. */
    onconfirm: () => Promise<void>;
    /** At the icon size, Delete and Cancel show an icon. Confirm delete keeps its text. */
    size?: 'default' | 'icon';
  }

  const { name, onconfirm, size = 'default' }: Props = $props();

  let confirming = $state(false);
  const toggleName = $derived(`${confirming ? 'Cancel delete' : 'Delete'} ${name}`);

  // The second click of a double click carries detail 2, and a key press carries detail 0.
  // Limitation: the guard ignores one deliberate Delete then Confirm delete inside the
  // double-click interval at one spot. It fails toward no delete.
  // Limitation: after a confirmed delete, the focus goes to the body, because the row or the
  // keyed control leaves.
  function confirm(event: MouseEvent): void {
    if (event.detail > 1) return;
    confirming = false;
    void onconfirm();
  }
</script>

<!-- One click must not delete. Delete and Cancel are one element, so the focus stays on it between the steps. -->
<Button
  variant={confirming ? 'secondary' : 'ghost'}
  {size}
  aria-label={toggleName}
  title={toggleName}
  onclick={() => (confirming = !confirming)}
>
  {#if size === 'default'}
    {confirming ? 'Cancel' : 'Delete'}
  {:else if confirming}
    <X aria-hidden="true" class="size-4" />
  {:else}
    <Trash aria-hidden="true" class="size-4" />
  {/if}
</Button>
{#if confirming}
  <Button variant="danger" aria-label="Confirm delete {name}" onclick={confirm}
    >Confirm delete</Button
  >
{/if}
