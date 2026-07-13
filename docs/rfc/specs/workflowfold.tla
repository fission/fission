---------------------------- MODULE workflowfold ----------------------------
(***************************************************************************)
(* The RFC-0022 workflow engine's CAS-append event-log fold.               *)
(*                                                                         *)
(* A run is a linear sequence of task steps 1..NumSteps, each retried up   *)
(* to MaxAttempts.  Several reconcilers race: each reads the log, computes *)
(* the next event from the fold of what it read, then appends with         *)
(* compare-and-swap on the log length (RFC-0021 EventLog.Append with       *)
(* expectedSeq).  A reconciler may crash between read and append (its      *)
(* planned event is lost) — modeled by Crash.  Function invocation outcome *)
(* is nondeterministic (ok / fail), and because a re-read of a             *)
(* scheduled-but-unfinished step re-invokes, execution is at-least-once by *)
(* construction; what the CAS discipline must guarantee is that the LOG    *)
(* stays consistent: no duplicate schedules, at most one result per        *)
(* attempt, exactly one terminal event, nothing after it.                  *)
(***************************************************************************)
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS
  NumSteps,     \* steps in the workflow, executed 1..NumSteps
  MaxAttempts,  \* attempts per step before the run fails
  Reconcilers   \* reconciler ids (model values)

ASSUME NumSteps \in Nat \ {0}
ASSUME MaxAttempts \in Nat \ {0}

NoEvent == [type |-> "none", step |-> 0, attempt |-> 0]

Ev(t, s, a) == [type |-> t, step |-> s, attempt |-> a]

TerminalTypes == {"done", "failed", "cancelled"}

VARIABLES
  log,        \* Seq of events — the EventLog stream for this run
  snap,       \* [Reconcilers -> Nat] log length at last read (expectedSeq)
  planned,    \* [Reconcilers -> event] computed-but-not-yet-appended event
  cancelReq   \* BOOLEAN: fission.io/cancel-requested annotation observed

vars == <<log, snap, planned, cancelReq>>

-----------------------------------------------------------------------------
(* Fold helpers: everything is derived from the log alone. *)

Idx == 1..Len(log)

HasEv(t, s, a) == \E i \in Idx: log[i] = Ev(t, s, a)

StepOk(s) == \E i \in Idx: log[i].type = "ok" /\ log[i].step = s

Terminal == \E i \in Idx: log[i].type \in TerminalTypes

SchedAttempts(s) == {log[i].attempt : i \in {j \in Idx: log[j].type = "sched" /\ log[j].step = s}}

Max(S) == CHOOSE x \in S: \A y \in S: y <= x

AttemptsOf(s) == IF SchedAttempts(s) = {} THEN 0 ELSE Max(SchedAttempts(s))

HasResult(s, a) == HasEv("ok", s, a) \/ HasEv("fail", s, a)

(* First step not yet completed; 0 when the whole sequence is done. *)
CurStep ==
  IF \A s \in 1..NumSteps: StepOk(s) THEN 0
  ELSE CHOOSE s \in 1..NumSteps: ~StepOk(s) /\ \A p \in 1..(s - 1): StepOk(p)

(* The set of events a reconciler may plan from the current log — the     *)
(* engine's transition function.  Result events come in an {ok, fail}     *)
(* pair because the function-invocation outcome is nondeterministic.      *)
NextOptions ==
  IF Terminal THEN {}
  ELSE IF cancelReq THEN {Ev("cancelled", 0, 0)}
  ELSE LET s == CurStep IN
       IF s = 0 THEN {Ev("done", 0, 0)}
       ELSE LET a == AttemptsOf(s) IN
            IF a = 0 THEN {Ev("sched", s, 1)}
            ELSE IF ~HasResult(s, a)
                 THEN {Ev("ok", s, a), Ev("fail", s, a)}   \* (re-)invoke
                 ELSE IF a < MaxAttempts
                      THEN {Ev("sched", s, a + 1)}          \* retry
                      ELSE {Ev("failed", 0, 0)}             \* exhausted

-----------------------------------------------------------------------------

Init ==
  /\ log = <<>>
  /\ snap = [r \in Reconcilers |-> 0]
  /\ planned = [r \in Reconcilers |-> NoEvent]
  /\ cancelReq = FALSE

(* Read the log, compute the next event.  Reading and computing are one   *)
(* atomic step here because the engine computes from the snapshot it      *)
(* read; the race window this spec exercises is between Read and Append.  *)
Read(r) ==
  /\ planned[r] = NoEvent
  /\ NextOptions # {}
  /\ \E e \in NextOptions:
       planned' = [planned EXCEPT ![r] = e]
  /\ snap' = [snap EXCEPT ![r] = Len(log)]
  /\ UNCHANGED <<log, cancelReq>>

(* EventLog.Append with expectedSeq: succeeds only if the log has not     *)
(* grown since Read; otherwise it is a no-op and the reconciler replans.  *)
TryAppend(r) ==
  /\ planned[r] # NoEvent
  /\ IF Len(log) = snap[r]
     THEN log' = Append(log, planned[r])
     ELSE log' = log
  /\ planned' = [planned EXCEPT ![r] = NoEvent]
  /\ UNCHANGED <<snap, cancelReq>>

(* Reconciler crash/restart: planned work is lost, nothing else changes.  *)
Crash(r) ==
  /\ planned[r] # NoEvent
  /\ planned' = [planned EXCEPT ![r] = NoEvent]
  /\ UNCHANGED <<log, snap, cancelReq>>

Cancel ==
  /\ ~cancelReq
  /\ cancelReq' = TRUE
  /\ UNCHANGED <<log, snap, planned>>

Next ==
  \/ \E r \in Reconcilers: Read(r) \/ TryAppend(r) \/ Crash(r)
  \/ Cancel

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(* Invariants — the numbered invariants of RFC-0022. *)

TypeOK ==
  /\ log \in Seq([type: {"sched", "ok", "fail", "done", "failed", "cancelled"},
                  step: 0..NumSteps, attempt: 0..MaxAttempts])
  /\ snap \in [Reconcilers -> Nat]
  /\ cancelReq \in BOOLEAN

(* W1: a (step, attempt) is scheduled at most once — no double execution  *)
(* windows are OPENED twice (execution itself is at-least-once, but the   *)
(* log never records the same attempt scheduled twice).                   *)
NoDupSched ==
  \A i, j \in Idx:
    (i < j /\ log[i].type = "sched" /\ log[j].type = "sched"
     /\ log[i].step = log[j].step /\ log[i].attempt = log[j].attempt) => FALSE

(* W2: at most one result per (step, attempt) — a duplicate/conflicting   *)
(* completion from a raced re-invocation never lands.                     *)
AtMostOneResult ==
  \A i, j \in Idx:
    (i < j
     /\ log[i].type \in {"ok", "fail"} /\ log[j].type \in {"ok", "fail"}
     /\ log[i].step = log[j].step /\ log[i].attempt = log[j].attempt) => FALSE

(* W3: results only for attempts that were scheduled first. *)
ResultHasSched ==
  \A j \in Idx:
    log[j].type \in {"ok", "fail"} =>
      \E i \in Idx: i < j /\ log[i] = Ev("sched", log[j].step, log[j].attempt)

(* W4: terminal stability — a terminal event is the last event; nothing   *)
(* is ever appended after it (this is what CAS-on-seq buys).              *)
TerminalIsLast ==
  \A i \in Idx: log[i].type \in TerminalTypes => i = Len(log)

(* W5: retries are sequential — attempt a+1 is scheduled only after a     *)
(* recorded failure of attempt a.                                         *)
RetryAfterFail ==
  \A j \in Idx:
    (log[j].type = "sched" /\ log[j].attempt > 1) =>
      \E i \in Idx: i < j /\ log[i] = Ev("fail", log[j].step, log[j].attempt - 1)

(* W6: attempt budget respected. *)
AttemptsBounded ==
  \A i \in Idx: log[i].type = "sched" => log[i].attempt <= MaxAttempts

=============================================================================
