Committed as `04805b6f0`.

RBE apps now borrow the shared DB without migrating or closing it. Executors remain unchanged.

- Integration suite and sample users passed 5/5.
- Avoided 67 migrations; measured ~18 ms saved per app setup.
- No overall suite speedup established.
- One timeout-test failure occurred; subsequent checks passed.

Details in [result.md](/home/exedev/repo/result.md), left uncommitted. Remote URL restored; nothing pushed.