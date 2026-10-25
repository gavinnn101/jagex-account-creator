<script lang="ts">
  interface Props {
    label: string;
    /** The value and the text of each option, in order. */
    options: { value: string; text: string }[];
    /** The field name of the value in a form submit. */
    name?: string;
    value?: string;
  }

  let { label, options, name, value = $bindable() }: Props = $props();
  const id = $props.id();
</script>

<!-- h-8 is the height of a Button, so a select and a button line up in a row. -->
<div class="flex flex-col gap-1">
  <label for={id} class="text-sm font-semibold">{label}</label>
  <select
    {id}
    {name}
    class="h-8 w-full min-w-0 rounded-md border border-line-strong bg-surface px-2"
    bind:value
  >
    {#each options as option (option.value)}
      <option value={option.value}>{option.text}</option>
    {/each}
  </select>
</div>
