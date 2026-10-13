Committed `2ca41d583`: replaced Survey with Promptui, preserving cache, cancellation, and terminal behavior.

Picker tests and PTY checks passed. Direct-user builds passed; integration tests failed because they require 3 CPUs on this 2-CPU VM.

The library graph has 49 fewer external labels. Shared dependency files remain unchanged.

Details: [result.md](/home/exedev/repo/cli/remotebazel/result.md).