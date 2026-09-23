---------------------------- MODULE WAL ----------------------------
(* A key-value store backed by a write-ahead log. A crash truncates
   the log: the store comes back holding the contents as of the last
   Sync plus some prefix of the records written since. *)
EXTENDS Naturals, Sequences

VARIABLES
    kv,      \* the contents, as Get and Snapshot see them
    synced,  \* the contents as of the last Sync (ghost)
    pending  \* the records written since the last Sync (ghost)

vars == <<kv, synced, pending>>

Apply(m, r) ==
    IF r.del
    THEN [k \in DOMAIN m \ {r.key} |-> m[k]]
    ELSE [k \in DOMAIN m \cup {r.key} |-> IF k = r.key THEN r.val ELSE m[k]]

RECURSIVE Replay(_, _)
Replay(m, log) == IF log = <<>> THEN m ELSE Replay(Apply(m, Head(log)), Tail(log))

Init ==
    /\ kv = <<>>
    /\ synced = <<>>
    /\ pending = <<>>

Write(r) ==
    /\ pending' = Append(pending, r)
    /\ kv' = Apply(kv, r)
    /\ UNCHANGED synced

Put(k, v) == Write([key |-> k, val |-> v, del |-> FALSE])

Delete(k) == Write([key |-> k, val |-> "", del |-> TRUE])

Get(k, v, ok) ==
    /\ ok = (k \in DOMAIN kv)
    /\ ok => v = kv[k]
    /\ UNCHANGED vars

Sync ==
    /\ synced' = kv
    /\ pending' = <<>>
    /\ UNCHANGED kv

\* A clean close syncs.
Close == Sync

Crash ==
    \E n \in 0..Len(pending) :
        /\ kv' = Replay(synced, SubSeq(pending, 1, n))
        /\ synced' = kv'
        /\ pending' = <<>>
====================================================================
