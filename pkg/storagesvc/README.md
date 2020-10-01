# StorageSvc

StorageSvc consists of 3 components

- Storage http request handler
- StowClient
- ArchivePruner

## StorageSvc

This is the HTTP handler that serves requests to :

- upload archive into a storage
- fetch an archive from storage
- delete archive from storage

## StowClient

This is the storage interface layer that interacts with stow package.
It provides methods to:

- write a file to storage
- retrieve a file from storage
- delete a file from storage
- get all files on storage

## ArchivePruner

This acts like a cron job to clean up orphaned archives from storage.
By default configured to run every hour. The value can be set in Values.yaml to any preferred interval.
