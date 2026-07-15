---------------------------- MODULE eventlogsub ----------------------------
(***************************************************************************)
(* Topic-subscription protocol of RFC-0027 statestore-backed eventing:     *)
(* publishers AppendAny to an EventLog stream, one subscription's consumer *)
(* instances (which overlap during mqt leadership transitions) deliver     *)
(* events in order from a durable KV cursor and advance it with a          *)
(* version-CAS write, and a reaper trims the stream to the committed       *)
(* cursor (the min-cursor retention rule, single-subscription form).       *)
(*                                                                         *)
(* Models: publish, consumer read/deliver/commit, consumer crash (local    *)
(* progress lost, redelivery on restart), poison events (terminal          *)
(* handling = ErrorTopic publish, then advance — RFC-0027 E5), and the     *)
(* CAS guard on cursor commits (KVStore SetOptions.IfVersion).             *)
(*                                                                         *)
(* NOT modeled: MaxRetries counting (a poison event's retry loop is        *)
(* collapsed into its terminal ErrorTopic handling); the age/size          *)
(* retention backstop (it intentionally violates delivery completeness     *)
(* for a stalled subscriber — documented loss, outside the green model);   *)
(* multiple subscriptions (each has an independent cursor record, so one   *)
(* subscription is the general case for cursor safety; min-cursor across   *)
(* subscriptions only tightens the reaper's trim bound).                   *)
(*                                                                         *)
(* Run with CasGuard = TRUE  : all invariants hold (the design).           *)
(* Run with CasGuard = FALSE : TLC finds the blind-write trace — an        *)
(* overlapping consumer that read an older cursor commits its smaller      *)
(* progress over a newer commit, regressing the cursor                     *)
(* (CursorMonotonic) — proving the cursor write MUST be a KV              *)
(* version-CAS (SetOptions.IfVersion), not a blind Set.                    *)
(*                                                                         *)
(* A cursor regression is not data loss (NoLostHandling still holds —      *)
(* at-least-once tolerates redelivery) but it unbounds redelivery and,     *)
(* raced with the reaper, strands the subscription below the trim          *)
(* horizon; monotonicity is the property the driver owes the protocol.     *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS
  Consumers,   \* consumer-instance ids (model values; >1 models overlap)
  MaxEvents,   \* stream length bound (state-space bound only)
  Poison,      \* events whose delivery always fails terminally
  CasGuard     \* TRUE = cursor commits are version-CAS guarded (the design)

ASSUME MaxEvents \in Nat \ {0}
ASSUME Poison \subseteq 1..MaxEvents

NoLocal == [has |-> FALSE, rc |-> 0, rv |-> 0, prog |-> 0]

VARIABLES
  head,       \* stream head: events 1..head exist
  trimmed,    \* events 1..trimmed are reaped (gone)
  cursor,     \* the durable KV cursor value (last handled seq)
  cver,       \* the cursor record's KV version (the CAS token)
  cursorHi,   \* history: highest cursor ever committed (for CursorMonotonic)
  delivered,  \* events delivered to the function at least once
  errored,    \* events terminally routed to ErrorTopic (poison path)
  local       \* [Consumers -> [has, rc, rv, prog]] per-instance view/progress

vars == <<head, trimmed, cursor, cver, cursorHi, delivered, errored, local>>

TypeOK ==
  /\ head \in 0..MaxEvents
  /\ trimmed \in 0..MaxEvents
  /\ cursor \in 0..MaxEvents
  /\ cver \in Nat
  /\ cursorHi \in 0..MaxEvents
  /\ delivered \subseteq 1..MaxEvents
  /\ errored \subseteq 1..MaxEvents
  /\ local \in [Consumers -> [has: BOOLEAN, rc: 0..MaxEvents,
                              rv: Nat, prog: 0..MaxEvents]]

Init ==
  /\ head = 0
  /\ trimmed = 0
  /\ cursor = 0
  /\ cver = 0
  /\ cursorHi = 0
  /\ delivered = {}
  /\ errored = {}
  /\ local = [c \in Consumers |-> NoLocal]

(* A publisher appends at the current head (AppendAny): no CAS loop, the   *)
(* store assigns the next seq atomically.                                  *)
Publish ==
  /\ head < MaxEvents
  /\ head' = head + 1
  /\ UNCHANGED <<trimmed, cursor, cver, cursorHi, delivered, errored, local>>

(* A consumer instance (re)reads the durable cursor record — on start,     *)
(* after a crash, or to refresh after losing a CAS race.                   *)
Read(c) ==
  /\ local' = [local EXCEPT ![c] = [has |-> TRUE, rc |-> cursor,
                                    rv |-> cver, prog |-> cursor]]
  /\ UNCHANGED <<head, trimmed, cursor, cver, cursorHi, delivered, errored>>

(* Deliver the next event after this instance's local progress.  A poison  *)
(* event's terminal handling is its ErrorTopic publish (E5): it is         *)
(* published BEFORE progress advances past it, so a crash in between       *)
(* redelivers the poison, never skips it.                                  *)
Deliver(c) ==
  LET e == local[c].prog + 1 IN
  /\ local[c].has
  /\ e <= head
  /\ e > trimmed
  /\ IF e \in Poison
       THEN /\ errored' = errored \cup {e}
            /\ UNCHANGED delivered
       ELSE /\ delivered' = delivered \cup {e}
            /\ UNCHANGED errored
  /\ local' = [local EXCEPT ![c].prog = e]
  /\ UNCHANGED <<head, trimmed, cursor, cver, cursorHi>>

(* Commit local progress to the durable cursor.  With CasGuard the write   *)
(* requires the instance's read version to still be current               *)
(* (SetOptions.IfVersion) — a loser must Read again first.  Without it,    *)
(* the write is blind (last-writer-wins), and an instance holding an old   *)
(* view can regress the cursor.                                            *)
Commit(c) ==
  /\ local[c].has
  /\ local[c].prog > local[c].rc
  /\ CasGuard => (local[c].rv = cver)
  /\ cursor' = local[c].prog
  /\ cver' = cver + 1
  /\ cursorHi' = IF cursor' > cursorHi THEN cursor' ELSE cursorHi
  /\ local' = [local EXCEPT ![c].rc = local[c].prog, ![c].rv = cver + 1]
  /\ UNCHANGED <<head, trimmed, delivered, errored>>

(* A consumer instance crashes or loses leadership: local progress since   *)
(* its last commit is lost; a successor (or itself, restarted) must Read.  *)
Crash(c) ==
  /\ local[c].has
  /\ local' = [local EXCEPT ![c] = NoLocal]
  /\ UNCHANGED <<head, trimmed, cursor, cver, cursorHi, delivered, errored>>

(* The retention reaper trims to the committed cursor (min-cursor rule).   *)
(* trimmed stays monotone even if the cursor regressed (unguarded model).  *)
Reap ==
  /\ cursor > trimmed
  /\ trimmed' = cursor
  /\ UNCHANGED <<head, cursor, cver, cursorHi, delivered, errored, local>>

Next ==
  \/ Publish
  \/ Reap
  \/ \E c \in Consumers: Read(c) \/ Deliver(c) \/ Commit(c) \/ Crash(c)

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
(* Invariants — the RFC-0027 E-invariants in checkable form.               *)

(* E2/E5 (no skip): everything at or below the committed cursor received   *)
(* terminal handling — delivered, or ErrorTopic'd if poison.               *)
NoLostHandling ==
  \A e \in 1..cursor: e \in (delivered \cup errored)

(* E5 (poison isolation): a poison event the cursor passed was ErrorTopic'd *)
(* (and never handed to the function as a success).                         *)
PoisonToErrorTopic ==
  /\ \A e \in Poison \cap (1..cursor): e \in errored
  /\ delivered \cap Poison = {}

(* E3 (no premature trim): nothing is reaped before its terminal handling. *)
NoTrimBeyondHandled ==
  \A e \in 1..trimmed: e \in (delivered \cup errored)

(* The cursor never regresses — the property the version-CAS exists for.   *)
(* Violated when CasGuard = FALSE (the blind-write negative model).        *)
CursorMonotonic == cursor = cursorHi

(* State-space bound for TLC (versions grow with commits).                 *)
StateConstraint == cver <= MaxEvents + 3

=============================================================================
