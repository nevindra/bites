# permessage-deflate is not supported for now

WebSocket compression is not in milestone 2. With context takeover, each socket keeps a compression window of 32 KiB or more, about ten times the 3.6 KB an idle socket costs ([`docs/research/ws-idle/`](../research/ws-idle/report.md)). It ships in milestone 4 ([`docs/roadmap.md`](../roadmap.md)) in the no-context-takeover mode only, which costs CPU a message instead of memory a socket, with its cost measured before it ships. Context takeover stays off: a client cannot be made to pay ten times an idle socket's memory for a smaller frame.

**What was rejected:** the first version of this ADR waited for a workload to ask. A chat application that sends repetitive JSON is common enough that compression belongs on the list a finished framework carries.
