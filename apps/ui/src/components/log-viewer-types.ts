/** One row of the LogViewer. In its own module because `<script setup>` cannot export types. */
export type LogRow = {
  key: string | number;
  /** Never wraps. Time of day for job logs, the service name for compose logs. */
  gutter?: string;
  /** Tooltip on the gutter — the full timestamp the gutter abbreviates. */
  gutterTitle?: string;
  text: string;
  /** Colours the row: error/warn from job levels, or a stable per-service hue class. */
  tone?: string;
  /** A separator row — rendered as a centred rule with a label (a date change). */
  separator?: boolean;
};
