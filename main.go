package main

import (
	"context"
	"fmt"
	"go/format"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/filemode"
	"gopkg.in/src-d/go-git.v4/storage/memory"

	"github.com/bradleyfalzon/ghinstallation"
	"github.com/google/go-github/github"
	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

// Shared transport to reuse TCP connections.
var tr = http.DefaultTransport
var appId int
var privateKey []byte

func main() {
	var err error
	if appId, err = strconv.Atoi(os.Getenv("GOFMT_APP_ID")); err != nil {
		log.Fatal(err)
	}
	privateKey = []byte(os.Getenv("GOFMT_PRIVATE_KEY"))
	appTrans, err := ghinstallation.NewAppsTransport(tr, appId, privateKey)
	if err != nil {
		log.Fatal(err)
	}
	client := github.NewClient(&http.Client{Transport: appTrans})
	scrapeRepos(client)
}

func scrapeRepos(client *github.Client) error {
	installs, _, err := client.Apps.ListInstallations(context.Background(), nil)
	if err != nil {
		return err
	}
	for _, install := range installs {
		if err = scrapeInstall(install); err != nil {
			return err
		}
	}
	return nil
}

func scrapeInstall(install *github.Installation) error {
	installTrans, err := ghinstallation.New(tr, appId, int(install.GetID()), privateKey)
	if err != nil {
		return err
	}
	token, err := installTrans.Token()
	if err != nil {
		return err
	}
	installClient := github.NewClient(&http.Client{Transport: installTrans})
	repos, _, err := installClient.Apps.ListRepos(context.Background(), nil)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		r := &repoChecker{
			repo:   repo,
			token:  token,
			client: installClient,
		}
		if err = r.run(); err != nil {
			return err
		}
	}
	return nil
}

type repoChecker struct {
	repo   *github.Repository
	client *github.Client
	token  string
	clone  *git.Repository
}

func (r *repoChecker) run() error {
	url := r.repo.GetCloneURL()
	basicAuth := fmt.Sprintf("https://x-access-token:%s@", r.token)
	url = strings.Replace(url, "https://", basicAuth, -1)
	fmt.Println(url)
	start := time.Now()
	repo, err := git.Clone(memory.NewStorage(), nil, &git.CloneOptions{
		URL:          url,
		SingleBranch: true,
		Depth:        1,
		NoCheckout:   true,
	})
	if err != nil {
		return err
	}
	log.Printf("CLONED in %s", time.Now().Sub(start))
	r.clone = repo
	r.walk()
	return nil
}

func (r *repoChecker) walk() {
	head, err := r.clone.Head()
	if err != nil {
		log.Fatal(err)
	}
	commit, err := r.clone.CommitObject(head.Hash())
	if err != nil {
		log.Fatal(err)
	}
	baseTree, err := commit.Tree()
	if err != nil {
		log.Fatal(err)
	}
	changed, newHash := r.walkTree("", baseTree)
	if changed {
		log.Println("Repo changed! Committing:")
		tree := newHash.String()
		parent := head.Hash().String()
		commit, _, err := r.client.Git.CreateCommit(context.Background(), r.repo.GetOwner().GetLogin(), r.repo.GetName(), &github.Commit{
			Message: &message,
			Tree: &github.Tree{
				SHA: &tree,
			},
			Parents: []github.Commit{
				{SHA: &parent},
			},
		})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("COMMMMMIITTT", commit.GetSHA())
		// finally create branch ref
		ref := "heads/gofmtbot"
		_, resp, err := r.client.Git.GetRef(context.Background(), r.repo.GetOwner().GetLogin(), r.repo.GetName(), ref)
		var branch *github.Reference
		reference := &github.Reference{
			Ref: &ref,
			Object: &github.GitObject{
				SHA: commit.SHA,
			},
		}
		if err != nil {
			if resp == nil || resp.StatusCode != 404 {
				log.Fatal(err)
			}
			branch, _, err = r.client.Git.CreateRef(context.Background(), r.repo.GetOwner().GetLogin(), r.repo.GetName(), reference)
		} else {
			branch, _, err = r.client.Git.UpdateRef(context.Background(), r.repo.GetOwner().GetLogin(), r.repo.GetName(), reference, true)
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("BRANNNCH", branch)
	}
}

var message = "Format go code"

func (r *repoChecker) walkTree(prefix string, tree *object.Tree) (changed bool, newHash plumbing.Hash) {
	//fmt.Println("WALK", prefix)
	changedEntries := []object.TreeEntry{}
	for _, e := range tree.Entries {
		entry := e
		switch entry.Mode {
		case filemode.Dir:
			subTree, err := r.clone.TreeObject(entry.Hash)
			if err != nil {
				log.Fatal(err)
			}
			changed, newHash := r.walkTree(prefix+"/"+entry.Name, subTree)
			if changed {
				entry.Hash = newHash
				changedEntries = append(changedEntries, entry)
			}
		case filemode.Regular, filemode.Executable:
			if strings.HasSuffix(entry.Name, ".go") {
				changed, newHash := r.walkBlob(entry.Hash)
				if changed {
					//log.Printf("%s: %s -> %s", prefix+"/"+entry.Name, entry.Hash, newHash)
					entry.Hash = newHash
					changedEntries = append(changedEntries, entry)
				}
			}
		default:
		}
	}
	if len(changedEntries) == 0 {
		return false, plumbing.Hash{}
	}
	updates := []github.TreeEntry{}
	for _, e := range changedEntries {
		entry := e
		sha := entry.Hash.String()
		mode := entry.Mode.String()
		mode = strings.TrimPrefix(mode, "0")
		update := github.TreeEntry{
			Path: &entry.Name,
			SHA:  &sha,
			Mode: &mode,
		}
		updates = append(updates, update)
	}
	newTree, _, err := r.client.Git.CreateTree(context.Background(), r.repo.GetOwner().GetLogin(), r.repo.GetName(), tree.Hash.String(), updates)
	if err != nil {
		log.Fatal(err)
	}
	return true, plumbing.NewHash(newTree.GetSHA())
}

func (r *repoChecker) walkBlob(hash plumbing.Hash) (changed bool, newHash plumbing.Hash) {
	blob, err := r.clone.BlobObject(hash)
	if err != nil {
		log.Fatal(err)
	}
	reader, err := blob.Reader()
	if err != nil {
		log.Fatal(err)
	}
	source, err := ioutil.ReadAll(reader)
	if err != nil {
		log.Fatal(err)
	}
	formatted, err := format.Source(source)
	if err != nil {
		return false, plumbing.Hash{}
	}
	if string(formatted) != string(source) {
		blob := &github.Blob{}
		content := string(formatted)
		blob.Content = &content
		newBlob, _, err := r.client.Git.CreateBlob(context.Background(), r.repo.GetOwner().GetLogin(), r.repo.GetName(), blob)
		if err != nil {
			log.Fatal(err)
		}
		return true, plumbing.NewHash(newBlob.GetSHA())
	}
	return false, plumbing.Hash{}
}
