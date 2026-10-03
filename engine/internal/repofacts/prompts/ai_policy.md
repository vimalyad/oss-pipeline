A repository's contributor docs follow. Decide its policy on AI-assisted
contributions. Output ONLY JSON:

{"bans": true|false, "requires_disclosure": true|false, "quote": "verbatim sentence or ''"}

- "bans": the project refuses AI-generated or AI-assisted contributions.
- "requires_disclosure": AI assistance is allowed but must be declared.
- Both false if the text only mentions AI incidentally (for example the project
  IS an AI tool, or it bans AI-generated *issue reports* but not pull requests).
- "quote": the exact sentence you based this on, or "" if neither applies.
Be conservative: only set a flag on an explicit statement about contributions.

<docs>
{{docs}}
</docs>
