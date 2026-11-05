# The idle WebSocket gate

What was run: the built nilo-go server (`server/main.go`: one typed WebSocket route that answers a message with a message) holding 10,000 and then 20,000 WebSockets opened by `client/main.go`, which upgrades each and, in the `msg` runs, sends one message and reads the reply. The server ran on cores 2, 3, 10 and 11 and the client on 4 to 7 and 12 to 15 of a Ryzen 7 9700X, the client's sockets spread over source addresses 127.0.0.2 to 127.0.0.5, Go 1.27.3, at commit 6b086e4 with the step 12 changes in the working tree. The runs alternated `msg` and `nomsg`, three of each (`run.sh`), and the raw JSON is in `~/.cache/nilo-go-ws-gate/raw`. `analyze.py` makes the table.

| kind | runs | fails | RSS per socket, 0 to 10k | RSS marginal, 10k to 20k | heap plus stack per socket after two GCs at 20k | stacks per socket | goroutines at 20k |
|---|---|---|---|---|---|---|---|
| no message | 3 | 0 | 6,958 [6,888 to 7,095] | 4,628 [4,624 to 4,631] | 3,841 [3,840 to 3,841] | 2,055 | 20,003 |
| one message exchanged | 3 | 0 | 7,032 [6,892 to 7,103] | 4,911 [4,840 to 5,047] | 3,944 [3,942 to 3,947] | 2,055 [2,053 to 2,056] | 20,003 |

The prototype of `docs/research/ws-idle/` held 3,615 B of RSS and 2,947 B of heap plus stack marginal. The built socket is about 1 KB heavier in both; the split was not measured.

The gate in the test suite (`ws_idle_test.go`) measures 1,200 to 2,200 sockets in one process pair and read 3.4 to 3.8 KB of heap plus stack in six runs; its ceiling is 4,700 B, 20% over the 10k to 20k figure above. The plain and TLS gates were rerun beside it, three runs each against a copy of the working tree before the step: the plain gate read 3,294 to 3,640 B after and 3,294 to 3,337 B before, inside the spread.
