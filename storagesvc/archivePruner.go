package storagesvc

import (
	"time"
	"log"
)

type ArchivePruner struct {
	//archiveCache ArchiveCache // TODO: Come back
	crdClient CRDClient
	archiveChan chan(string)
	stowClient *StowClient
}

func MakeArchivePruner(stowClient *StowClient) *ArchivePruner {
	return &ArchivePruner{
		crdClient: MakeCRDClient(),
		archiveChan: make(chan string),
		stowClient: stowClient,
	}
}

func (pruner *ArchivePruner) pruneArchives() {
	for {
		select {
		case archiveID := <- pruner.archiveChan:
			// read archiveIDs from archiveChan and issue a delete request on them
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

/*
  Everytime a function is updated, a new package is created, leaving the pkg that the function referenced earlier as orphan.
  Also, the archives that are pointed to by these orphan pkgs can be deleted from the storage.
  This method fetches archives from such orphan pkgs.

  TODO : From earlier discussion, we dont need it. Instead we might change the way function update works today.
  Just need clarification one more time.
 */
func (pruner *ArchivePruner) getArchiveFromOrphanedPkgs() {
	// kubPackages := get all pkgs from kubernetes

	// kubPackages might contain packages created by user less than an hour ago, still not referenced by a function
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

func (pruner *ArchivePruner) Start() {
	ticker := time.NewTicker(60 * time.Second) // TODO : Interval configurable in helm chart. Fed in to the pod through env variable.
	go pruner.pruneArchives()
	for {
		select {
		case <- ticker.C:
			// These methods fetch unused archive IDs and send them to archiveChannel
			go pruner.getArchiveFromOrphanedPkgs()
			go pruner.getOrphanedArchives()
		}
	}
}