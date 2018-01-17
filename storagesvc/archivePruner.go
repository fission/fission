package storagesvc

import "time"

type ArchivePruner struct {
	archiveCache ArchiveCache
	crdClient CRDClient
	archiveChan chan(string)
	ss StorageService
}

func (pruner *ArchivePruner) pruneArchives() {
	var archiveID string
	ticker := time.NewTicker(60 * time.Second) // TODO : Interval configurable in helm chart. Fed in to the pod through env variable.
	for {
		select {
			case <- ticker.C:
				archiveID <- pruner.archiveChan
				if err := pruner.ss.container.RemoveItem(archiveID); err != nil {
					// logging the error and continuing with other deletions.
					// hopefully this archive will be deleted in the next iteration.
					// log.Errorf("err: %v deleting archive: %s from storage", err, archiveID)
				}

		}
	}
}

func (pruner *ArchivePruner) insertArchive(archiveID string) {
	pruner.archiveChan <- archiveID
}

func (pruner *ArchivePruner) getArchiveFromOrphanedPkgs() {
	// kubPackages := get all pkgs from kubernetes

	// funcRefPackages := get all pkgs referenced by functions

	// orphanedPkgs := kubPackages - funcRefPackages

	// for item in orphanedPkgs; extract archiveID, insertArchive(archiveID);
}

/*
   A user may have deleted pkgs with kubectl or fission cli. That only deletes the crd.Package object from kubernetes
   database and not the archives that are referenced in it, leaving the archives as orphans.
   This method reaps those orphaned archives.
 */
func (pruner *ArchivePruner) getOrphanedArchives() {
	// archiveInPkgs := make([]{})

	// pkgs := get all pkgs from kubernetes
	// for item in pkgs; extract archiveID, append(archivesInPkgs, archiveID)

	// archivesInStorage := get all archives on storage

	// orphanedArchives := archivesInStorage - archivesInPkgs

	// for item in orphanedArchives; insertArchive(item);

}