A repository's contributor docs follow. Extract the rules that decide whether
an outside pull request is ELIGIBLE AT ALL. Output ONLY JSON:

{"required_issue_labels": [...], "forbidden_issue_labels": [...], "quote": "..."}

- "required_issue_labels": labels a linked issue MUST carry for a pull request
  to be accepted. Example: "We accept external pull requests only for issues
  labelled `help wanted`" -> ["help wanted"].
- "forbidden_issue_labels": labels that make an issue off limits. Example:
  "Do not open pull requests for any issue marked `core`" -> ["core"].
- "quote": the sentence you took this from, verbatim, or "".
- Both lists empty if the docs state no such rule. Do NOT infer a rule from a
  project merely using a label; it must be stated as a requirement.
Label names exactly as written, without backticks.

<docs>
{{docs}}
</docs>
