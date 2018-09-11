package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/src-d/go-git.v4/plumbing/filemode"

	"github.com/bradleyfalzon/ghinstallation"
	"github.com/google/go-github/github"
	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/storage/memory"
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
		fmt.Println(install.GetID(), install.GetAccount().GetLogin())
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
			url := repo.GetCloneURL()
			basicAuth := fmt.Sprintf("https://x-access-token:%s@", token)
			url = strings.Replace(url, "https://", basicAuth, -1)
			fmt.Println(url)
			r, err := git.Clone(memory.NewStorage(), nil, &git.CloneOptions{
				URL:          url,
				SingleBranch: true,
				Depth:        1,
			})
			if err != nil {
				return err
			}
			walk(r)
		}
	}
	return nil
}

func walk(r *git.Repository) {
	head, err := r.Head()
	if err != nil {
		log.Fatal(err)
	}
	commit, err := r.CommitObject(head.Hash())
	if err != nil {
		log.Fatal(err)
	}
	baseTree, err := commit.Tree()
	if err != nil {
		log.Fatal(err)
	}
	walkInner("", baseTree, r)
}

func walkInner(prefix string, tree *object.Tree, r *git.Repository) {
	for _, entry := range tree.Entries {
		switch entry.Mode {
		case filemode.Dir:
			subTree, err := r.TreeObject(entry.Hash)
			if err != nil {
				log.Fatal(err)
			}
			walkInner(filepath.Join(prefix, entry.Name), subTree, r)
		case filemode.Regular:
			if strings.HasSuffix(entry.Name, ".go") {
				fmt.Println(filepath.Join(prefix, entry.Name))
			}
		default:
		}
	}
}
