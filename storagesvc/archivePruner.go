package storagesvc

import (
	"time"
	log "github.com/sirupsen/logrus"
)

type ArchivePruner struct {
	crdClient *CRDClient
	archiveChan chan(string)
	stowClient *StowClient
	pruneInterval int
}

const defaultPruneInterval int = 60 // in minutes

func MakeArchivePruner(stowClient *StowClient, pruneInterval int) *ArchivePruner {
	return &ArchivePruner {
		crdClient: MakeCRDClient(),
		archiveChan: make(chan string),
		stowClient: stowClient,
		pruneInterval: pruneInterval,
	}
}

// This method listens to archiveChannel for archive ids that need to be deleted
func (pruner *ArchivePruner) pruneArchives() {
	log.Debug("starting loop to prune archives..")
	for {
		select {
		case archiveID := <- pruner.archiveChan:
			log.WithField("archive ID", archiveID).Debug("sending delete request")
			if err := pruner.stowClient.removeFileByID(archiveID); err != nil {
				// logging the error and continuing with other deletions.
				// hopefully this archive will be deleted in the next iteration.
				log.WithField("archive ID", archiveID).WithError(err).Error("Error deleting archive")
			}
		}
	}
}

// This method just writes the archive ID into the channel.
func (pruner *ArchivePruner) insertArchive(archiveID string) {
	pruner.archiveChan <- archiveID
}

// A user may have deleted pkgs with kubectl or fission cli. That only deletes crd.Package objects from kubernetes
// and not the archives that are referenced by them, leaving the archives as orphans.
// This method reaps the orphaned archives.
func (pruner *ArchivePruner) getOrphanArchives() {
	log.Debug("get orphan archives")
	archivesRefByPkgs := make([]string, 0)
	var archiveID string

	// get all pkgs from kubernetes
	pkgs, err := pruner.crdClient.getPkgList()
	if err != nil {
		// Safe to just silence the error here. Hoping next iteration will succeed.
		log.WithError(err).Error("Error getting package list from kubernetes")
		return
	}

	// extract archives referenced by these pkgs
	for _, pkg := range pkgs {
		if pkg.Spec.Deployment.URL != "" {
			archiveID = utilGetQueryParamValue(pkg.Spec.Deployment.URL, "id")
			archivesRefByPkgs = append(archivesRefByPkgs, archiveID)
		}
		if pkg.Spec.Source.URL != ""{
			archiveID = utilGetQueryParamValue(pkg.Spec.Source.URL, "id")
			archivesRefByPkgs = append(archivesRefByPkgs, archiveID)
		}
	}

	log.WithField("list", "archives referenced by packages").Debugf("%s", archivesRefByPkgs)

	// get all archives on storage
	// out of them, there may be some just created but not referenced by packages yet.
	// need to filter them out.
	archivesInStorage, err := pruner.stowClient.getItemIDsWithFilter(filterItemCreatedAMinuteAgo, time.Now())
	if err != nil {
		// Safe to just silence the error here. Hoping next iteration will succeed.
		log.WithError(err).Error("Error getting items from storage")
		return
	}
	log.WithField("list", "archives in storage").Debugf("%s", archivesInStorage)

	// difference of the two lists gives us the list of orphan archives. This is just a brute force approach.
	// need to do something more optimal at scale.
	orphanedArchives := utilGetDifferenceOfLists(archivesInStorage, archivesRefByPkgs)
	log.WithField("list", "orphan archives").Debugf("%s", orphanedArchives)

	// send each orphan archive away for deletion
	for _, archiveID = range orphanedArchives {
		pruner.insertArchive(archiveID)
	}
}

// This method starts a go routine that listens to a channel for archive IDs that need to deleted.
// Also wakes up at regular intervals to make a list of archive IDs that need to be reaped
// and sends them over to the channel for deletion
func (pruner *ArchivePruner) Start() {
	ticker := time.NewTicker(time.Duration(pruner.pruneInterval) * time.Second)
	go pruner.pruneArchives()
	for {
		select {
		case <- ticker.C:
			// This method fetches unused archive IDs and sends them to archiveChannel for deletion
			go pruner.getOrphanArchives()
		}
	}
}