---
name: bug-fix
intent: Fix a bug, crash, regression or failing test by reproducing it first, fixing the cause and proving the fix with a test.
match: [bug, broken, crash, error, regression, repro, reproduce, fails, failing, wrong]
skills: []
---

Find the cause before changing code, and keep the change as small as the fix.

1. Restate the bug in one sentence: what happens, and what should happen instead.
2. Reproduce it. Write a failing test (or a short script) that shows the bug, and run it to see it fail.
3. Trace the failure to its cause. Read the code paths involved; don't guess from the symptom.
4. Fix the cause with the smallest change that makes sense. Leave unrelated code alone.
5. Run the reproduction again and watch it pass, then run the wider test suite.
6. Look for the same mistake nearby and mention any you find instead of fixing them silently.
7. Summarize: the cause, the fix, and the test that now guards it.

## Done when

The reproduction test fails without the fix and passes with it, and the existing tests still pass.
