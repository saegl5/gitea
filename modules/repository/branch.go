// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"

	"code.gitea.io/gitea/models/db"
	git_model "code.gitea.io/gitea/models/git"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/modules/container"
	"code.gitea.io/gitea/modules/git"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/timeutil"
)

// SyncRepoBranches synchronizes branch table with repository branches
func SyncRepoBranches(ctx context.Context, repoID, doerID int64) (int64, error) {
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return 0, err
	}

	log.Debug("SyncRepoBranches: in Repo[%d:%s]", repo.ID, repo.FullName())

	gitRepo, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		log.Error("OpenRepository[%s]: %w", repo.RepoPath(), err)
		return 0, err
	}
	defer gitRepo.Close()

	return SyncRepoBranchesWithRepo(ctx, repo, gitRepo, doerID)
}

func SyncRepoBranchesWithRepo(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, doerID int64) (int64, error) {
	allBranches := container.Set[string]{}
	{
		branches, _, err := gitRepo.GetBranchNames(0, 0)
		if err != nil {
			return 0, err
		}
		log.Trace("SyncRepoBranches[%s]: branches[%d]: %v", repo.FullName(), len(branches), branches)
		for _, branch := range branches {
			allBranches.Add(branch)
		}
	}

	dbBranches := make(map[string]*git_model.Branch)
	{
		branches, err := git_model.FindBranches(ctx, git_model.FindBranchOptions{
			ListOptions: db.ListOptions{
				ListAll: true,
			},
			RepoID: repo.ID,
		})
		if err != nil {
			return 0, err
		}
		for _, branch := range branches {
			dbBranches[branch.Name] = branch
		}
	}

	var toAdd []*git_model.Branch
	var toUpdate []*git_model.Branch
	var toRemove []int64
	for branch := range allBranches {
		dbb := dbBranches[branch]
		commit, err := gitRepo.GetBranchCommit(branch)
		if err != nil {
			return 0, err
		}
		if dbb == nil {
			toAdd = append(toAdd, &git_model.Branch{
				RepoID:        repo.ID,
				Name:          branch,
				CommitID:      commit.ID.String(),
				CommitMessage: commit.CommitMessage,
				PusherID:      doerID,
				CommitTime:    timeutil.TimeStamp(commit.Author.When.Unix()),
			})
		} else if commit.ID.String() != dbb.CommitID {
			toUpdate = append(toUpdate, &git_model.Branch{
				ID:            dbb.ID,
				RepoID:        repo.ID,
				Name:          branch,
				CommitID:      commit.ID.String(),
				CommitMessage: commit.CommitMessage,
				PusherID:      doerID,
				CommitTime:    timeutil.TimeStamp(commit.Author.When.Unix()),
			})
		}
	}

	for _, dbBranch := range dbBranches {
		if !allBranches.Contains(dbBranch.Name) && !dbBranch.IsDeleted {
			toRemove = append(toRemove, dbBranch.ID)
		}
	}

	log.Trace("SyncRepoBranches[%s]: toAdd: %v, toUpdate: %v, toRemove: %v", repo.FullName(), toAdd, toUpdate, toRemove)

	if len(toAdd) == 0 && len(toRemove) == 0 && len(toUpdate) == 0 {
		return int64(len(allBranches)), nil
	}

	if err := db.WithTx(ctx, func(subCtx context.Context) error {
		if len(toAdd) > 0 {
			if err := git_model.AddBranches(subCtx, toAdd); err != nil {
				return err
			}
		}

		for _, b := range toUpdate {
			if _, err := db.GetEngine(subCtx).ID(b.ID).
				Cols("commit_id, commit_message, pusher_id, commit_time, is_deleted").
				Update(b); err != nil {
				return err
			}
		}

		if len(toRemove) > 0 {
			if err := git_model.DeleteBranches(subCtx, repo.ID, doerID, toRemove); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		return 0, err
	}
	return int64(len(allBranches)), nil
}
