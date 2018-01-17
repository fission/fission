package storagesvc

import (
	"time"
)

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
				// These methods fetch unused archive IDs and send them to archiveChannel
				go pruner.getArchiveFromOrphanedPkgs()
				go pruner.getOrphanedArchives()

				// read archiveIDs from archiveChan and issue a delete request on them
				archiveID <- pruner.archiveChan
				if err := pruner.ss.storageClient.RemoveItem(archiveID); err != nil {
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

	// kubPackages might contain packages created less than an hour ago, still not referenced by a function
	// filter out those pkgs using pkg metadata.

	// funcRefPackages := get all pkgs referenced by functions

	// orphanedPkgs := kubPackages - funcRefPackages

	// for item in orphanedPkgs; extract archiveID, insertArchive(archiveID);
}

/*
   A user may have deleted pkgs with kubectl or fission cli. That only deletes crd.Package objects from kubernetes
   and not the archives that are referenced in them, leaving the archives as orphans.
   This method reaps those orphaned archives.
 */
func (pruner *ArchivePruner) getOrphanedArchives() {
	// archiveInPkgs := make([]{})

	// pkgs := get all pkgs from kubernetes
	// for item in pkgs; extract archiveID, append(archivesInPkgs, archiveID)

	// archivesInStorage := get all archives on storage which are older than an hour ago.
	/*
	TODO : Create a method to do this in StorageService
	err = stow.Walk(containers[0], stow.NoPrefix, 100, func(item stow.Item, err error) error {
		if err != nil {
			return err
		}
		log.Println(item.Name())
		return nil
	})
	if err != nil {
		return err
	}
	 */

	// orphanedArchives := archivesInStorage - archivesInPkgs

	// for item in orphanedArchives; insertArchive(item);

}