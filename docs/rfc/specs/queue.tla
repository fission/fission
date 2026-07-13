------------------------------- MODULE queue -------------------------------
(***************************************************************************)
(* Lease/settle lifecycle of the RFC-0021 statestore Queue, as consumed   *)
(* by RFC-0024 async invocation.                                           *)
(*                                                                         *)
(* Models: enqueue-at-init, lease with visibility timeout, lease expiry    *)
(* and re-lease, dead-lettering on the retry path (nack-at-budget and       *)
(* expiry-at-budget, SQS maxReceiveCount), and the *lease epoch* guard on   *)
(* settles.  Dispatchers may hold a delivery past their lease (slow worker  *)
(* / stale process) — the guard is what stops a stale settle from           *)
(* corrupting a newer delivery.                                            *)
(*                                                                         *)
(* NOT modeled: the driver's Kill(receipt) — a permanent-failure escape    *)
(* hatch (RFC-0024's 4xx path) that dead-letters regardless of attempts.   *)
(* It sits outside the retry protocol, so DeadImpliesExhausted (which holds *)
(* for nack/expiry dead-lettering here) does not constrain it by design.    *)
(*                                                                         *)
(* Run with EpochGuard = TRUE  : all invariants hold (the design).         *)
(* Run with EpochGuard = FALSE : TLC finds the stale-settle trace — a      *)
(* zombie dispatcher's Kill/Ack decides the outcome while the CURRENT      *)
(* lease's delivery is still in flight (NoOrphanedCurrentDelivery) —       *)
(* proving the Postgres driver MUST carry an epoch column and guard        *)
(* UPDATE/DELETE on it, not settle by message id alone.                    *)
(*                                                                         *)
(* Checking note (found by TLC, kept as documentation): the stronger       *)
(* property "a message whose work EVER succeeded is never dead-lettered"   *)
(* is NOT achievable under at-least-once + visibility timeouts, guard or   *)
(* no guard: a delivery that succeeds slower than its lease has its ack    *)
(* correctly rejected as stale, and the retry may fail and dead-letter.    *)
(* SQS/Lambda share these semantics.  This is why RFC-0024 requires the    *)
(* lease duration to exceed the function timeout, and why DLQ redrive      *)
(* must tolerate already-succeeded work.                                   *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS
  Msgs,         \* message ids (model values)
  Dispatchers,  \* dispatcher ids (model values)
  NoOne,        \* sentinel: no holder (model value)
  MaxAttempts,  \* deliveries before a failed settle dead-letters
  LeaseTTL,     \* lease duration in clock ticks
  MaxTime,      \* clock bound (state-space bound only)
  EpochGuard    \* TRUE = settles guarded by lease epoch (the design)

ASSUME MaxAttempts \in Nat \ {0}
ASSUME LeaseTTL \in Nat \ {0}
ASSUME MaxTime \in Nat

VARIABLES
  state,      \* [Msgs -> {"queued","leased","acked","dead"}]
  visibleAt,  \* [Msgs -> Nat]  earliest tick the message may be leased
  attempts,   \* [Msgs -> Nat]  deliveries started so far
  epoch,      \* [Msgs -> Nat]  bumped on every lease
  holder,     \* [Msgs -> Dispatchers \cup {NoOne}]
  expiry,     \* [Msgs -> Nat]  current lease expiry tick
  inflight,   \* set of <<dispatcher, msg, epoch>> deliveries being worked
  settles,    \* [Msgs -> Nat]  effective terminal settles (must stay <= 1)
  now         \* the clock

vars == <<state, visibleAt, attempts, epoch, holder, expiry,
          inflight, settles, now>>

TypeOK ==
  /\ state \in [Msgs -> {"queued", "leased", "acked", "dead"}]
  /\ visibleAt \in [Msgs -> Nat]
  /\ attempts \in [Msgs -> Nat]
  /\ epoch \in [Msgs -> Nat]
  /\ holder \in [Msgs -> Dispatchers \cup {NoOne}]
  /\ expiry \in [Msgs -> Nat]
  /\ inflight \subseteq (Dispatchers \X Msgs \X Nat)
  /\ settles \in [Msgs -> Nat]
  /\ now \in Nat

Init ==
  /\ state = [m \in Msgs |-> "queued"]
  /\ visibleAt = [m \in Msgs |-> 0]
  /\ attempts = [m \in Msgs |-> 0]
  /\ epoch = [m \in Msgs |-> 0]
  /\ holder = [m \in Msgs |-> NoOne]
  /\ expiry = [m \in Msgs |-> 0]
  /\ inflight = {}
  /\ settles = [m \in Msgs |-> 0]
  /\ now = 0

(* SELECT ... FOR UPDATE SKIP LOCKED + visible_at check + epoch bump. *)
Lease(d, m) ==
  /\ state[m] = "queued"
  /\ now >= visibleAt[m]
  /\ attempts[m] < MaxAttempts
  /\ state' = [state EXCEPT ![m] = "leased"]
  /\ epoch' = [epoch EXCEPT ![m] = @ + 1]
  /\ holder' = [holder EXCEPT ![m] = d]
  /\ expiry' = [expiry EXCEPT ![m] = now + LeaseTTL]
  /\ attempts' = [attempts EXCEPT ![m] = @ + 1]
  /\ inflight' = inflight \cup {<<d, m, epoch[m] + 1>>}
  /\ UNCHANGED <<visibleAt, settles, now>>

(* Visibility timeout.  A message whose attempt budget is spent (every      *)
(* delivery expired without a settle — e.g. a repeatedly-crashing worker)   *)
(* is dead-lettered here, matching SQS maxReceiveCount semantics, so it is   *)
(* never stranded.  The epoch is bumped on that transition so a still-       *)
(* inflight stale delivery no longer matches the current epoch, keeping      *)
(* NoOrphanedCurrentDelivery valid.  Any other expired lease returns to      *)
(* "queued" for re-lease; the old delivery stays inflight — the slow/stale   *)
(* worker this spec is about.                                               *)
Expire(m) ==
  /\ state[m] = "leased"
  /\ now >= expiry[m]
  /\ IF attempts[m] >= MaxAttempts
     THEN /\ state' = [state EXCEPT ![m] = "dead"]
          /\ settles' = [settles EXCEPT ![m] = @ + 1]
          /\ epoch' = [epoch EXCEPT ![m] = @ + 1]
          /\ holder' = [holder EXCEPT ![m] = NoOne]
          /\ UNCHANGED visibleAt
     ELSE /\ state' = [state EXCEPT ![m] = "queued"]
          /\ visibleAt' = [visibleAt EXCEPT ![m] = now]
          /\ holder' = [holder EXCEPT ![m] = NoOne]
          /\ UNCHANGED <<settles, epoch>>
  /\ UNCHANGED <<attempts, expiry, inflight, now>>

(* The settle guard.  Guarded (the design): only the current lease may    *)
(* settle.  Unguarded (the bug): any settle lands while the row exists.   *)
CanSettle(m, ep) ==
  IF EpochGuard
  THEN state[m] = "leased" /\ epoch[m] = ep
  ELSE state[m] \in {"queued", "leased"}

(* Dispatcher finished a delivery and its work SUCCEEDED -> Ack. *)
Ack(d, m, ep) ==
  /\ <<d, m, ep>> \in inflight
  /\ inflight' = inflight \ {<<d, m, ep>>}
  /\ IF CanSettle(m, ep)
     THEN /\ state' = [state EXCEPT ![m] = "acked"]
          /\ settles' = [settles EXCEPT ![m] = @ + 1]
          /\ holder' = [holder EXCEPT ![m] = NoOne]
     ELSE UNCHANGED <<state, settles, holder>>
  /\ UNCHANGED <<visibleAt, attempts, epoch, expiry, now>>

(* Dispatcher finished a delivery and its work FAILED -> Nack (requeue    *)
(* with backoff) or Kill (dead-letter) when attempts are exhausted.       *)
NackOrKill(d, m, ep) ==
  /\ <<d, m, ep>> \in inflight
  /\ inflight' = inflight \ {<<d, m, ep>>}
  /\ IF CanSettle(m, ep)
     THEN IF attempts[m] >= MaxAttempts
          THEN /\ state' = [state EXCEPT ![m] = "dead"]
               /\ settles' = [settles EXCEPT ![m] = @ + 1]
               /\ holder' = [holder EXCEPT ![m] = NoOne]
               /\ UNCHANGED visibleAt
          ELSE /\ state' = [state EXCEPT ![m] = "queued"]
               /\ visibleAt' = [visibleAt EXCEPT ![m] = now + 1]
               /\ holder' = [holder EXCEPT ![m] = NoOne]
               /\ UNCHANGED settles
     ELSE UNCHANGED <<state, visibleAt, settles, holder>>
  /\ UNCHANGED <<attempts, epoch, expiry, now>>

Tick ==
  /\ now < MaxTime
  /\ now' = now + 1
  /\ UNCHANGED <<state, visibleAt, attempts, epoch, holder, expiry,
                 inflight, settles>>

Next ==
  \/ \E d \in Dispatchers, m \in Msgs: Lease(d, m)
  \/ \E m \in Msgs: Expire(m)
  \/ \E t \in inflight: Ack(t[1], t[2], t[3])
  \/ \E t \in inflight: NackOrKill(t[1], t[2], t[3])
  \/ Tick

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(* Invariants — the numbered invariants of RFC-0021 / RFC-0024. *)

(* I1: a message reaches a terminal settle at most once. *)
SettledAtMostOnce == \A m \in Msgs: settles[m] <= 1

(* I2: only the CURRENT lease decides the outcome — a message never       *)
(* reaches a terminal state while the current epoch's delivery is still   *)
(* in flight (i.e. a stale settle never decides for a live delivery).     *)
(* This is the invariant the epoch guard exists for: with                 *)
(* EpochGuard = FALSE, a zombie's Kill/Ack strands the live delivery and  *)
(* TLC produces the trace.                                                *)
NoOrphanedCurrentDelivery ==
  \A m \in Msgs:
    state[m] \in {"acked", "dead"} =>
      ~\E t \in inflight: t[2] = m /\ t[3] = epoch[m]

(* I3: dead-lettering only after the attempt budget is spent. *)
DeadImpliesExhausted == \A m \in Msgs: state[m] = "dead" => attempts[m] >= MaxAttempts

(* I4: at most MaxAttempts deliveries ever start. *)
AttemptsBounded == \A m \in Msgs: attempts[m] <= MaxAttempts

(* I5: a leased message has a holder; a non-leased one does not. *)
HolderConsistent ==
  \A m \in Msgs: (state[m] = "leased") <=> (holder[m] # NoOne)

=============================================================================
