# What a Topic costs in memory

The question, from an application that gives each User a Topic of their own beside one per room, Space and conversation: how much does a Topic cost, idle and per Socket in it, so a design can count them.

`cost_test.go` opens 2000 sockets over bitetest's in-memory connections, then per round measures the live heap (three `runtime.GC` before each `ReadMemStats`) around: 2000 zero `Topic` values; 2000 `NewTopic` over `bitetest.Broker` with no Socket; subscribing each socket to its own brokered Topic (the first Socket of a Topic, which also starts its broker subscription); and 2000 sockets joining one room after the first. Every object of every round is kept alive, so a free does not land inside a later measurement. Five rounds, the first two set aside for warm-up (map growth from the previous round's frees made one negative).

`TestTopicsKeyCost` measures, the same way, 2000 keys of one brokered `Topics` each subscribing one socket (the Topic of the key, its entry in the family and the functions that drop it), what is left once every key's socket has unsubscribed, and 2000 sockets of a zero Topic given a `SubscribeUnless` function after a plain `Subscribe`. Each round uses a new `Topics`.

First run on an AMD Ryzen 7 9700X, Go 1.27.3, Linux, at commit 5115eeb with the working tree of 2026-11-04 (the payload plans cached per type), twice:

| What | Bytes, rounds 2 to 4 of two runs |
|---|---|
| a zero `Topic[Out]` | 104 |
| a `NewTopic` with no Socket | 304 |
| the first Socket of a brokered Topic (the subscriber map, the broker's subscription, the Socket's entry) | 288 to 485 |
| each further Socket in a Topic (a map entry, and the Topic in the Socket's own list) | 37 to 61 |

Run again at commit f7ac6c3 with the working tree of 2026-11-04 (`SubscribeUnless`, `Topics`), pinned to cores 2,3,10,11, three runs, rounds 3 and 4 of each:

| What | Bytes, three runs |
|---|---|
| a zero `Topic[Out]` (two more words: the `SubscribeUnless` map and the family's hook) | 120 |
| a `NewTopic` with no Socket | 320 |
| the first Socket of a brokered Topic | 312 |
| each further Socket in a Topic | 37 |
| a key of a brokered `Topics` with its one Socket | 783 |
| left once the last Socket of every key has left | 0 |
| a `SubscribeUnless` function on a Socket already in a Topic (its entry in the Topic's map of functions) | 37 |

The 0 came after two leaks were fixed in the same change, both found by this probe: `bitetest.Broker`'s stop marked a subscription dead and kept it, holding the Topic (613 B a key left), and the engine's `WS.Forget` shifted the Socket's list of Topics without clearing the last slot, so a Socket kept alive the last Topic it left (55 B a key left before the family dropped its emptied map too).

Before the plans were cached per `Out` type, each `NewTopic` compiled its own encoder and decoder; the cache makes the idle Topic's bytes its whole cost. The broker's subscription is `bitetest.Broker`'s; `pgnotify`'s is a struct of two functions and a flag in a map, of the same order. A key of a `Topics` is made when its first Socket joins and dropped when the last leaves, so it costs nothing while nobody is in it; a hand-made map of Topics (`examples/chat`) does the same with more code.

Reading it for a design: a socket in a Topic per User, one per Space and three per conversation costs about 5 × 300 B (if it is the first in each) or 5 × 50 B (if not), against the 1695 B an idle socket costs (`docs/research/optimisation-pass/report.md`).
