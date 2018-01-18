package storagesvc

import (
	"time"
	"log"
)

type ArchivePruner struct {
	archiveCache ArchiveCache
	crdClient CRDClient
	archiveChan chan(string)
	stowClient *StowClient
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
				if err := pruner.stowClient.removeFileByID(archiveID); err != nil {
					// logging the error and continuing with other deletions.
					// hopefully this archive will be deleted in the next iteration.
					log.Printf("err: %v deleting archiveID: %s from storage", err, archiveID)
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
	archivesRefByPkgs := make([]string, 0)
	var archiveID string

	// pkgs := get all pkgs from kubernetes
	pkgs, err := pruner.crdClient.getPkgList()
	if err != nil {

	}

	// for item in pkgs; extract archiveID, append(archivesInPkgs, archiveID)
	for _, pkg := range pkgs {
		if pkg.Spec.Deployment.URL {
			archiveID = utilGetQueryParamValue(pkg.Spec.Deployment.URL, "id")
			archivesRefByPkgs = append(archivesRefByPkgs, archiveID)
		}
		if pkg.Spec.Source.URL {
			archiveID = utilGetQueryParamValue(pkg.Spec.Source.URL, "id")
			archivesRefByPkgs = append(archivesRefByPkgs, archiveID)
		}
	}

	// get all archives on storage
	// TODO : out of all the archives on storage, there may be some just created but not referenced by packages yet. Need to filter them out here
	// We can either have a fixed time , ex : 1 hour and filter out those archives created within 1 hour.
	// Or, use an archiveCache. Insert archives into this cache everytime a createArchive is called.
	archivesInStorage, err := pruner.stowClient.getItems()

	// orphanedArchives := archivesInStorage - archivesInPkgs
	orphanedArchives := utilGetDifferenceOfLists(archivesInStorage, archivesRefByPkgs)

	// for item in orphanedArchives; insertArchive(item);
	for _, archiveID = range orphanedArchives{
		pruner.insertArchive(archiveID)
	}
}
