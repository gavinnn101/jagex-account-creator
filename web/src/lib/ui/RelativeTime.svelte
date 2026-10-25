<script lang="ts">
  import { fullTime, relativeTime } from '#lib/format.ts';

  interface Props {
    /** The time, as the API sends it. */
    iso: string;
    /** The time of the clock of the page. One clock for each page keeps one timer for all its times. */
    now: Date;
  }

  const { iso, now }: Props = $props();
</script>

<!-- The API can send more than 3 fraction digits, and a datetime value holds at most 3. -->
<time datetime={new Date(iso).toISOString()} title={fullTime(iso)} class="whitespace-nowrap"
  >{relativeTime(iso, now)}</time
>
