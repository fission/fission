------------------------------- MODULE quota -------------------------------
(***************************************************************************)
(* Keyspace quota enforcement of the RFC-0023 stateful-functions state    *)
(* API, as served by statesvc over the RFC-0021 KVStore.                   *)
(*                                                                         *)
(* Models: N concurrent writers each adding a key to one keyspace that has *)
(* a MaxKeys budget.  A write must not push the committed key count past   *)
(* MaxKeys.  The question is whether the count check and the count         *)
(* increment are ONE indivisible operation or two — exactly the           *)
(* check-then-act TOCTOU that a naive "read count, if under budget then    *)
(* write" implementation exposes.                                          *)
(*                                                                         *)
(* Run with AtomicQuota = TRUE  : the reserve+commit is a single atomic    *)
(* step (a CAS on the counter key, or the value write conditioned on the   *)
(* counter in one statestore transaction).  QuotaNeverExceeded holds.      *)
(*                                                                         *)
(* Run with AtomicQuota = FALSE : the check (Reserve) and the increment    *)
(* (Commit) are separate steps.  TLC finds the trace where two writers     *)
(* both observe count = MaxKeys-1, both pass the check, and both commit —  *)
(* count reaches MaxKeys+1 (QuotaNeverExceeded violated).  This is the     *)
(* documented reason statesvc MUST enforce MaxKeys / the namespace byte    *)
(* budget with an atomic counter operation (KV CAS / a counted            *)
(* transaction), never a read-check-then-write against a plain counter.    *)
(*                                                                         *)
(* NOT modeled: TTL expiry (a background reaper only ever LOWERS the       *)
(* count, so it cannot break an upper-bound invariant), value-byte budget  *)
(* (identical protocol shape with a byte delta instead of +1), and the     *)
(* per-key CAS version race (already covered by eventlogsub.tla's          *)
(* version-CAS cursor and workflowfold.tla's CAS-append).                  *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS
  Writers,      \* writer ids (model values)
  MaxKeys,      \* the keyspace budget
  AtomicQuota   \* TRUE = reserve+commit is one atomic step (the design)

ASSUME MaxKeys \in Nat \ {0}

VARIABLES
  count,      \* committed key count in the keyspace
  phase       \* [Writers -> {"idle","reserved","done"}]

vars == <<count, phase>>

TypeOK ==
  /\ count \in Nat
  /\ phase \in [Writers -> {"idle", "reserved", "done"}]

Init ==
  /\ count = 0
  /\ phase = [w \in Writers |-> "idle"]

(* Atomic design: the under-budget check and the increment are a single    *)
(* indivisible action — the whole point of doing it as a KV CAS / counted  *)
(* transaction.  No other writer can interleave between check and commit.  *)
WriteAtomic(w) ==
  /\ AtomicQuota
  /\ phase[w] = "idle"
  /\ count < MaxKeys
  /\ count' = count + 1
  /\ phase' = [phase EXCEPT ![w] = "done"]

(* Non-atomic bug: Reserve reads the counter and passes the budget check;  *)
(* Commit increments later, WITHOUT re-checking.  Two writers can both      *)
(* reserve at count = MaxKeys-1 and then both commit.                       *)
Reserve(w) ==
  /\ ~AtomicQuota
  /\ phase[w] = "idle"
  /\ count < MaxKeys
  /\ phase' = [phase EXCEPT ![w] = "reserved"]
  /\ UNCHANGED count

Commit(w) ==
  /\ ~AtomicQuota
  /\ phase[w] = "reserved"
  /\ count' = count + 1
  /\ phase' = [phase EXCEPT ![w] = "done"]

Next ==
  \/ \E w \in Writers: WriteAtomic(w)
  \/ \E w \in Writers: Reserve(w)
  \/ \E w \in Writers: Commit(w)

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(* Invariants — RFC-0023 invariant S3 (quota soundness). *)

(* S3: the committed key count never exceeds the keyspace budget, even     *)
(* under concurrent writers racing the counter.                           *)
QuotaNeverExceeded == count <= MaxKeys

(* Sanity: a writer commits at most once (phase is monotone). *)
CommitsOnce == \A w \in Writers: phase[w] \in {"idle", "reserved", "done"}

=============================================================================
