# Package pruning design

## Resources to prune 

All functions have a pkg reference. This can be a package with either source and a deploy archives, or, a deploy archive. Everytime a function is updated, a new package is created.
So the archives that are pointed to by old pkg reference can be deleted from the storage.
Also, the pkg objects themselves can be deleted from kubernetes.


## Methods to identify the archives and packages not used anymore

But to find out the pkgs that are not referenced anymore, we would, as a brute force method:
1. list all pkgs
2. list all pkg names referenced by functions.
3. the difference of these 2 lists gives us all orphaned pkgs.
4. For each orphaned pkg, extract the URL for the archives and send a delete request to storageSvc to delete them

But the 2nd step above would be a little clumsy in that we need to first do a get of all functions and extract the pkg reference in each.
Instead, a better performing solution would be to mark packages that are not used (during function update step) with an annotation perhaps called "orphaned".
So, we could completely eliminate steps 2 and 3 above. Just perform the following steps :

1. range over each of the pkg definitions and look for the value of the annotation.
2. If "orphaned", extract the URL for the archives and send a delete request to storageSvc to delete them





 
    
