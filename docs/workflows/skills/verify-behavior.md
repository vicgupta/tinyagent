# Skill: verify-behavior
> Intent: Run the built program and check its observed output against the task's behavioral acceptance criteria.

**Inputs**
- run-tests
- task
- repo_path

Tests passing is not the same as the program doing what the task asked. This stage checks behavior from the outside.

1. Read the task and its acceptance criteria. A criterion is behavioral when it says what the built thing does when run: what a command prints, which exit code it returns, what a server responds. Code-structure, test or docs criteria are not behavioral.
2. If there are no behavioral criteria, write no behavior file and say so. The stage is skipped.
3. Otherwise write one case per behavioral criterion. Each case runs the real artifact (for example `go run ./cmd/app ...`, or a binary built outside the repository such as under `/tmp`) and states the exit code and the exact strings or patterns the criterion requires. Take expected strings from the criteria, not from what the program prints today.
4. Don't change product code in this stage. If a case fails, report which criterion it shows is unmet.
5. Never leave build output (binaries, coverage files) in the repository.

**Success check:** every behavioral acceptance criterion has a case, and every case's observed output matches its expectations.
