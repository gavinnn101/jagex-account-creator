<script lang="ts">
  interface Props {
    /** The accessible name of the alert, for example "Preset errors". */
    name: string;
    /** The messages to show. An empty list renders nothing. */
    messages: string[];
  }

  const { name, messages }: Props = $props();
</script>

{#if messages.length > 0}
  <!-- The alert sits on a wrapper, so the list keeps its list role. -->
  <div role="alert" aria-label={name}>
    <ul class="list-disc pl-5 text-danger">
      {#each messages as message, i (i)}
        <li>{message}</li>
      {/each}
    </ul>
  </div>
{/if}
