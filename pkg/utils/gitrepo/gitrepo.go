/*
Copyright 2022 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gitrepo

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/pkg/errors"
)

type GitRepo struct {
	setupDone       bool
	isGitRepo       bool
	repo            *git.Repository
	status          git.Status
	dirPath         string
	gitRepoRootPath string // absolute path of the root of the git repository
	commitID        string // commit ID the HEAD of the repository is pointing to
}

// NewGitRepo creates new GitRepo struct
// checks if the given directory path is part of git repository
// accordingly updates the fields of GitRepo struct and returns it
func NewGitRepo(dirPath string) *GitRepo {

	var g *GitRepo = &GitRepo{}
	var err error

	g.dirPath = dirPath
	g.setupDone = true

	// check if directory is tracked by git repo
	g.repo, err = git.PlainOpenWithOptions(dirPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return g
	}

	workTree, err := g.repo.Worktree()
	if err != nil {
		return g
	}

	g.status, err = workTree.Status()
	if err != nil {
		return g
	}

	if g.status == nil {
		return g
	}

	plumbRef, err := g.repo.Head()
	if err != nil {
		return g
	}

	// extract short commit ID
	g.commitID = plumbRef.Hash().String()[:7]

	// get the root path of git repository
	g.gitRepoRootPath = workTree.Filesystem.Root()

	g.isGitRepo = true
	return g
}

// IsGitRepo returns if the initialized directory path is tracked by git repo
func (g *GitRepo) IsGitRepo() bool {
	return g.setupDone && g.isGitRepo
}

// getFileCommitLabel returns the value of 'commit' label
// for the resources present in the file
/*
The value of the `commit` label for different status of the file is as follows:
| Git File Status                       | Label Value          |
|---------------------------------------|----------------------|
| New untracked file                    | untracked            |
| New staged file                       | staged               |
| Tracked file with changes in worktree | <commitID>-unstaged  |
| Tracked file with changes staged      | <commitID>-staged    |
| Tracked file with clean commit        | <commitID>           |
*/
func (g *GitRepo) GetFileCommitLabel(filePath string) (string, error) {

	if !g.setupDone {
		return "", errors.New(`GitRepo is not setup. It has to be created by calling 'NewGitRepo' function, 
					then this function has to be called.`)
	}

	if !g.isGitRepo {
		return "", errors.Errorf(`directory: %s doesn't belong to git repository.`, g.dirPath)
	}

	// filepath in the git repository
	splitPathList := strings.Split(filePath, g.gitRepoRootPath+string(os.PathSeparator))
	if len(splitPathList) != 2 {
		return "", errors.Errorf("error finding the git repository path of %s", filePath)
	}
	gitFilePath := splitPathList[1]
	gitFileStatus := g.status.File(gitFilePath)

	// check if file is tracked
	if !g.status.IsUntracked(gitFilePath) {

		// unstaged file
		if gitFileStatus.Worktree == git.Modified {
			return fmt.Sprintf("%s-unstaged", g.commitID), nil
		}

		// newly staged file
		if gitFileStatus.Staging == git.Added {
			return "staged", nil
		}

		// staged file
		if gitFileStatus.Staging == git.Modified {
			return fmt.Sprintf("%s-staged", g.commitID), nil
		}

	}

	// check if file is committed already
	oc, err := g.repo.Log(&git.LogOptions{FileName: &gitFilePath})
	if err != nil {
		return "", nil
	}

	_, err = oc.Next()
	defer oc.Close()

	if err != nil {
		// File not tracked by git and/or not committed
		return "untracked", nil

	}

	// File tracked by git and has been committed
	return g.commitID, nil
}
