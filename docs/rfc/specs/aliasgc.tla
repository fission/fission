------------------------------ MODULE aliasgc ------------------------------
(***************************************************************************)
(* Retention-GC vs alias-create race of the RFC-0025 function versions /   *)
(* aliases model.                                                          *)
(*                                                                         *)
(* Two controllers act on the same versions concurrently:                  *)
(*   - the alias path repoints an alias onto a version (admission checks    *)
(*     the version exists), and                                            *)
(*   - retention GC deletes an old, unaliased version.                     *)
(*                                                                         *)
(* GC is two-phase like any list-then-delete controller: it Scans (decides *)
(* a version is unaliased on the state it observes) and later Commits the   *)
(* delete.  The question is whether Commit RE-CHECKS the alias references   *)
(* at delete time or acts on the stale scan decision — exactly the         *)
(* list-snapshot TOCTOU between the GC sweep and a concurrent alias create. *)
(*                                                                         *)
(* Run with RecheckGuard = TRUE  : Commit re-checks "no alias references    *)
(* this version" as part of the delete (the design: the controller         *)
(* re-reads / the delete is guarded by a finalizer or an ownerRef the       *)
(* alias holds).  NoDanglingAlias holds.                                    *)
(*                                                                         *)
(* Run with RecheckGuard = FALSE : Commit deletes on the stale scan.  TLC   *)
(* finds the trace where an alias-create for version v commits between GC's *)
(* Scan(v) (saw v unaliased) and GC's Commit — the version is deleted out   *)
(* from under the live alias (NoDanglingAlias violated).  This is the       *)
(* documented reason retention GC must re-check alias references inside the *)
(* delete step (or gate delete on an alias-held finalizer/ownerRef), never  *)
(* act on the snapshot taken at the start of the sweep.                     *)
(*                                                                         *)
(* Corollary covered here: the alias admission check alone (webhook: "the   *)
(* target version exists") is NOT sufficient — admission passing does not   *)
(* keep the version alive against a concurrent GC.  The delete-time guard   *)
(* is the load-bearing one.                                                 *)
(*                                                                         *)
(* NOT modeled: multiple aliases (one suffices to strand), version publish  *)
(* (versions pre-exist), weighted secondary targets (each is an alias       *)
(* reference of the same shape), and the CRD ownerRef cascade on Function   *)
(* delete (a separate, non-racy path).                                     *)
(***************************************************************************)
EXTENDS FiniteSets

CONSTANTS
  Versions,      \* version ids (model values)
  NoOne,         \* sentinel: alias points at nothing (model value)
  RecheckGuard   \* TRUE = GC Commit re-checks alias refs (the design)

VARIABLES
  exists,        \* subset of Versions still present
  aliasTarget,   \* a version in exists, or NoOne
  gcTarget       \* the version GC has scan-decided to delete, or NoOne

vars == <<exists, aliasTarget, gcTarget>>

TypeOK ==
  /\ exists \subseteq Versions
  /\ aliasTarget \in Versions \cup {NoOne}
  /\ gcTarget \in Versions \cup {NoOne}

Init ==
  /\ exists = Versions
  /\ aliasTarget = NoOne
  /\ gcTarget = NoOne

(* Alias create/repoint.  Admission requires the target version to exist    *)
(* (the webhook's alias->version reference check).  This is the only guard  *)
(* on the alias side.                                                       *)
AliasPoint(v) ==
  /\ v \in exists
  /\ aliasTarget' = v
  /\ UNCHANGED <<exists, gcTarget>>

(* GC phase 1: pick an existing, currently-unaliased version and mark it    *)
(* for deletion.  This is the sweep's list+filter step.                     *)
GCScan(v) ==
  /\ gcTarget = NoOne
  /\ v \in exists
  /\ v # aliasTarget
  /\ gcTarget' = v
  /\ UNCHANGED <<exists, aliasTarget>>

(* GC phase 2: delete the scanned version.  Guarded (the design): the       *)
(* delete only fires if the version is STILL unaliased at commit time.      *)
(* Unguarded (the bug): it deletes on the stale scan decision.              *)
GCCommit ==
  /\ gcTarget # NoOne
  /\ gcTarget \in exists
  /\ (RecheckGuard => gcTarget # aliasTarget)
  /\ exists' = exists \ {gcTarget}
  /\ gcTarget' = NoOne
  /\ UNCHANGED aliasTarget

(* A guarded commit whose re-check fails abandons the delete (the version   *)
(* got aliased after the scan) and clears the target.                       *)
GCAbandon ==
  /\ gcTarget # NoOne
  /\ RecheckGuard
  /\ gcTarget = aliasTarget
  /\ gcTarget' = NoOne
  /\ UNCHANGED <<exists, aliasTarget>>

Next ==
  \/ \E v \in Versions: AliasPoint(v)
  \/ \E v \in Versions: GCScan(v)
  \/ GCCommit
  \/ GCAbandon

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(* Invariants — RFC-0025 invariants V2 (no dangling aliases) and V3 (GC    *)
(* safety).                                                                 *)

(* V2/V3: an alias never points at a version that has been deleted — the   *)
(* alias always resolves.                                                   *)
NoDanglingAlias == aliasTarget # NoOne => aliasTarget \in exists

=============================================================================
