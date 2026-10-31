---
name: investigate
intent: Investigate, explain or diagnose how something works or why something happens, and report findings without changing any files.
match: [investigate, explain, why, how, understand, diagnose, explore, where, find, research]
skills: []
read_only: true
---

Answer the question from evidence. Change nothing.

1. Restate the question and what a good answer looks like.
2. Find the code, config, logs or docs that bear on it. Search widely before reading deeply.
3. Read the relevant parts closely and follow the calls that matter.
4. Form an explanation and check it against the evidence; run read-only commands if they help.
5. Note what is certain, what is likely and what is unknown.
6. Report the answer with file and line references, and suggest next steps if the user wants to act.

## Done when

The question is answered with references to the evidence, and no files were changed.
