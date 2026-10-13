Committed as `d1f118db5`: resets now use quoted SQL on the control database, with forced disconnection and real migrations preserved.

All four targets passed 5/5 runs; case sets match. Three targets showed faster resets; usage results were mixed. Its existing ClickHouse-only skip is documented.

Measurements, case lists, and limitations are in [result.md](/home/exedev/repo/result.md), left uncommitted. Origin restored; nothing pushed.