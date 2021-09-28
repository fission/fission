# Fission release

## Prerequisites

1. Get access to:

   a. [The fission dockerhub account](https://hub.docker.com/r/fission/), if you have access, you will see Fission listed in [your organizations](https://hub.docker.com/organizations/)
   b. [Fission-charts repo](https://github.com/fission/fission-charts)
   c. [Fission Documentation Repo](https://github.com/fission/fission.io)
   d. [Fission main repo](https://github.com/fission/fission)

2. Get a Github [personal access token](https://help.github.com/articles/creating-a-personal-access-token-for-the-command-line/)

   Save the token to ~/.github-token (make sure that the file is readable only by you)

3. Go toolchain set up.

   The ability to build docker images.

   `docker login` with your own username; get this username added to
   the fission team on dockerhub.

4. Install changelog generator tool [github_changelog_generator](https://github.com/github-changelog-generator/github-changelog-generator)

5. Install realpath if you don't already have it : https://github.com/harto/realpath-osx (You can use the brew method on Mac)

6. Install [Github CLI](https://cli.github.com/)

## Check that the build is green

Fix the build if it is not green. DON'T proceed unless build is GREEN!

## Updating [Fission Repo](https://github.com/fission/fission)

1. cd to the top of the fission repo, create a branch:

   ```shell
   git checkout -b release-X.Y.Z
   ```

2. Change references of old version to new version in charts directory. You use replace and find all with a quick preview in your IDE.

3. Run

   `./hack/release.sh <VERSION>`

   This script checks to make sure you're releasing from the release-X.Y.Z
   branch, and that your repo is clean (no modified/staged files).

   This will take a while as it builds and pushes all images (Approx ~2-3 hours)

4. After the build is successful, the charts will be in build/charts directory from root of Fission. Move these charts to fission/fission-charts repo. We will push them shortly after some changes to repo.

5. As part of release script, changelog.md has been modified. Take a look at it, correct it if you want and then commit it, discard other directories etc. produced by builders after inspection.

6. Push this release-x.y.z branch to remote repo. Create a PR and wait for CI passed.

7. Now manually merge the release-x.y.z branch into master branch with Git command to prevent the commit SHA from being changed.

    `git checkout master && git merge --ff-only release-<VERSION>`

8. Test build from master branch for sanity check and make sure the master build is green

## Updating [Fission Charts](https://github.com/fission/fission-charts)

a. Switch to fission/fission-charts repo and run `index.sh` in that repo.  (Don't edit the index.yaml in this repo manually.  index.sh generates it, using the helm repo CLI.)

b. You should have index.yaml and fission-core-*, fission-all-* charts as change, add, commit and push it to repo.

1. Go to Github Web UI --> Releases, here you will notice a pre-release for the version x.y.z you just created.

Edit it and add links to:

a. Install guide in docs
b. Changelog.md

Before you save the release - UNCHECK the "This is a pre-release" checkbox. This mark the release as ready for consumption (If release is stable).

## Updating [Fission Docs](https://github.com/fission/fission.io)

1. Documentation Update

a. Merge documentation PRs that are peer reviewed and get latest master locally.

b. In the repo fission/fission.io change version in version.sh file to latest version (x.y.z) and run build.sh script

c. **ONLY** in the dist/x.y.z directory i.e. current version directory - replace all references from previous release to current release. Please use your IDE as there will be thousands of references.

d.VERIFY: There should be changes only in dist/x.y.z - where x.y.z is current version and in following files:

- dist/_redirects
- dist/index.html
- version.sh

## Announce the release

Announce the release on Fission Slack and Twitter.
