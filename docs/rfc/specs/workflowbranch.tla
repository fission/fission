--------------------------- MODULE workflowbranch ---------------------------
(***************************************************************************)
(* The RFC-0022 phase-3 parallel-region protocol: one Parallel/Map state   *)
(* fanning out into Branches concurrent branches over the same CAS-append  *)
(* event log.  workflowfold.tla verifies the LINEAR fold (sequential       *)
(* steps, retries, cancel, terminal stability); this module verifies what  *)
(* is genuinely new in phase 3 — concurrent branch execution, the join     *)
(* discipline, and fail-fast — on the smallest honest shape: each branch   *)
(* is one task step with attempts (branch-internal sequences compose per   *)
(* the linear spec).                                                       *)
(*                                                                         *)
(* Racing reconcilers read the log, compute the next event from the fold   *)
(* of what they read, and append with compare-and-swap on the log length.  *)
(* MaxConcurrency is deliberately NOT modeled: it throttles which branch   *)
(* actions are dispatched (an optimization), never which appends are       *)
(* legal (the protocol).                                                   *)
(***************************************************************************)
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS
  Branches,     \* number of parallel branches, 1..Branches
  MaxAttempts,  \* attempts per branch step before the run fails (fail-fast)
  Reconcilers   \* reconciler ids (model values)

ASSUME Branches \in Nat \ {0}
ASSUME MaxAttempts \in Nat \ {0}

NoEvent == [type |-> "none", branch |-> 0, attempt |-> 0]

Ev(t, b, a) == [type |-> t, branch |-> b, attempt |-> a]

TerminalTypes == {"done", "failed", "cancelled"}

VARIABLES
  log,        \* Seq of events — the run's EventLog stream
  snap,       \* [Reconcilers -> Nat] log length at last read (expectedSeq)
  planned,    \* [Reconcilers -> event] computed-but-not-yet-appended event
  cancelReq   \* BOOLEAN: fission.io/cancel-requested annotation observed

vars == <<log, snap, planned, cancelReq>>

-----------------------------------------------------------------------------
(* Fold helpers: everything is derived from the log alone. *)

Idx == 1..Len(log)

HasEv(t, b, a) == \E i \in Idx: log[i] = Ev(t, b, a)

Terminal == \E i \in Idx: log[i].type \in TerminalTypes

Joined == \E i \in Idx: log[i].type = "joined"

SchedAttempts(b) == {log[i].attempt : i \in {j \in Idx: log[j].type = "sched" /\ log[j].branch = b}}

Max(S) == CHOOSE x \in S: \A y \in S: y <= x

AttemptsOf(b) == IF SchedAttempts(b) = {} THEN 0 ELSE Max(SchedAttempts(b))

HasResult(b, a) == HasEv("ok", b, a) \/ HasEv("fail", b, a)

BranchOk(b) == \E a \in 1..MaxAttempts: HasEv("ok", b, a)

(* A branch is exhausted when its final permitted attempt has failed. *)
BranchExhausted(b) == HasEv("fail", b, MaxAttempts)

(* The set of events a reconciler may plan from the current log — the      *)
(* engine's transition function for a parallel region.  Every non-ok,      *)
(* non-exhausted branch offers its next action concurrently; result        *)
(* events come as {ok, fail} pairs because invocation outcomes are         *)
(* nondeterministic.  Fail-fast: one exhausted branch fails the run (the   *)
(* siblings' late completions then lose the CAS — W4).                     *)
BranchOptions(b) ==
  IF BranchOk(b) THEN {}
  ELSE LET a == AttemptsOf(b) IN
       IF a = 0 THEN {Ev("sched", b, 1)}
       ELSE IF ~HasResult(b, a)
            THEN {Ev("ok", b, a), Ev("fail", b, a)}   \* (re-)invoke
            ELSE IF a < MaxAttempts
                 THEN {Ev("sched", b, a + 1)}          \* retry
                 ELSE {}                               \* exhausted: run-level

NextOptions ==
  IF Terminal THEN {}
  ELSE IF cancelReq THEN {Ev("cancelled", 0, 0)}
  ELSE IF Joined THEN {Ev("done", 0, 0)}
  ELSE IF \E b \in 1..Branches: BranchExhausted(b) THEN {Ev("failed", 0, 0)}
  ELSE IF \A b \in 1..Branches: BranchOk(b) THEN {Ev("joined", 0, 0)}
  ELSE UNION {BranchOptions(b) : b \in 1..Branches}

-----------------------------------------------------------------------------

Init ==
  /\ log = <<>>
  /\ snap = [r \in Reconcilers |-> 0]
  /\ planned = [r \in Reconcilers |-> NoEvent]
  /\ cancelReq = FALSE

(* Read the log, compute the next event.  The race window this spec        *)
(* exercises is between Read and Append (same discipline as               *)
(* workflowfold.tla).                                                      *)
Read(r) ==
  /\ planned[r] = NoEvent
  /\ NextOptions # {}
  /\ \E e \in NextOptions:
       planned' = [planned EXCEPT ![r] = e]
  /\ snap' = [snap EXCEPT ![r] = Len(log)]
  /\ UNCHANGED <<log, cancelReq>>

(* EventLog.Append with expectedSeq: succeeds only if the log has not      *)
(* grown since Read; otherwise it is a no-op and the reconciler replans.   *)
TryAppend(r) ==
  /\ planned[r] # NoEvent
  /\ IF Len(log) = snap[r]
     THEN log' = Append(log, planned[r])
     ELSE log' = log
  /\ planned' = [planned EXCEPT ![r] = NoEvent]
  /\ UNCHANGED <<snap, cancelReq>>

(* Reconciler crash/restart: planned work is lost, nothing else changes.   *)
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
(* Invariants — W1-W6 lifted per branch, plus the join discipline. *)

TypeOK ==
  /\ log \in Seq([type: {"sched", "ok", "fail", "joined", "done", "failed", "cancelled"},
                  branch: 0..Branches, attempt: 0..MaxAttempts])
  /\ snap \in [Reconcilers -> Nat]
  /\ cancelReq \in BOOLEAN

(* W1b: a (branch, attempt) is scheduled at most once. *)
NoDupSched ==
  \A i, j \in Idx:
    (i < j /\ log[i].type = "sched" /\ log[j].type = "sched"
     /\ log[i].branch = log[j].branch /\ log[i].attempt = log[j].attempt) => FALSE

(* W2b: at most one result per (branch, attempt). *)
AtMostOneResult ==
  \A i, j \in Idx:
    (i < j
     /\ log[i].type \in {"ok", "fail"} /\ log[j].type \in {"ok", "fail"}
     /\ log[i].branch = log[j].branch /\ log[i].attempt = log[j].attempt) => FALSE

(* W3b: results only for attempts that were scheduled first. *)
ResultHasSched ==
  \A j \in Idx:
    log[j].type \in {"ok", "fail"} =>
      \E i \in Idx: i < j /\ log[i] = Ev("sched", log[j].branch, log[j].attempt)

(* W4: terminal stability — unchanged from the linear spec. *)
TerminalIsLast ==
  \A i \in Idx: log[i].type \in TerminalTypes => i = Len(log)

(* W5b: retries are sequential per branch. *)
RetryAfterFail ==
  \A j \in Idx:
    (log[j].type = "sched" /\ log[j].attempt > 1) =>
      \E i \in Idx: i < j /\ log[i] = Ev("fail", log[j].branch, log[j].attempt - 1)

(* W6b: attempt budget respected. *)
AttemptsBounded ==
  \A i \in Idx: log[i].type = "sched" => log[i].attempt <= MaxAttempts

(* W7: the join is unique and only ever follows every branch succeeding —  *)
(* a raced duplicate join, or a join before a straggler branch, never      *)
(* lands.                                                                  *)
JoinAfterAllBranches ==
  \A j \in Idx:
    log[j].type = "joined" =>
      /\ \A i \in Idx: (i # j) => log[i].type # "joined"
      /\ \A b \in 1..Branches:
           \E i \in Idx: i < j /\ log[i].type = "ok" /\ log[i].branch = b

(* W8: the join closes the region — after joined, only the terminal event  *)
(* may land (a late branch append always loses the CAS).                   *)
NothingAfterJoin ==
  \A i, j \in Idx:
    (i < j /\ log[i].type = "joined") => log[j].type \in TerminalTypes

=============================================================================
