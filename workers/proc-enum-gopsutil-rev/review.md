1. **Task accomplished:** Both packages now use gopsutil enumeration and `Ppid()`, and both BUILD edges to go-ps are removed. Enumeration errors propagate; individual parent-lookup failures are skipped. Shared dependency files are unchanged.

2. **Correctness bugs:** None found in the committed diff. There are no uncommitted changes. Existing validation logs report all seven selected test targets passed; I did not rerun Bazel.

3. **Missing tests:** Windows/macOS enumeration behavior remains untested. The Linux fixtures cover missing and unreadable stat files, but not a process actually exiting during enumeration. Both main regression tests skip entirely under root, also losing child/tree coverage there.

VERDICT: approve