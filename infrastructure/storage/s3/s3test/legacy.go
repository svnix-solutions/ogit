package s3test

import (
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/labbs/ogit/infrastructure/storage/s3"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// CreateLegacyRepository writes a repository in the layout used before packs
// (every object loose, with its type in metadata) under storageKey, and
// returns its commit. files become the root tree.
func CreateLegacyRepository(t testing.TB, ep Endpoint, storageKey string, files map[string]string) plumbing.Hash {
	t.Helper()
	client := awss3.New(awss3.Options{
		Region:       Region,
		BaseEndpoint: aws.String(ep.URL),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(ep.AccessKey, ep.SecretKey, ""),
	})
	st := s3.NewS3Storer(client, Bucket, "repositories/"+storageKey+".git", zerolog.Nop(), nil)

	store := func(o *plumbing.MemoryObject) plumbing.Hash {
		h, err := st.SetEncodedObject(o)
		require.NoError(t, err)
		return h
	}
	tree := &object.Tree{}
	for name, content := range files {
		blob := &plumbing.MemoryObject{}
		blob.SetType(plumbing.BlobObject)
		_, _ = blob.Write([]byte(content))
		tree.Entries = append(tree.Entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: store(blob)})
	}
	sort.Slice(tree.Entries, func(i, j int) bool { return tree.Entries[i].Name < tree.Entries[j].Name })
	treeObj := &plumbing.MemoryObject{}
	require.NoError(t, tree.Encode(treeObj))
	sig := object.Signature{Name: "Legacy", Email: "legacy@example.com", When: time.Unix(1700000000, 0)}
	commit := &object.Commit{Author: sig, Committer: sig, Message: "legacy layout\n", TreeHash: store(treeObj)}
	commitObj := &plumbing.MemoryObject{}
	require.NoError(t, commit.Encode(commitObj))
	commitHash := store(commitObj)

	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/heads/main", commitHash)))
	require.NoError(t, st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	return commitHash
}
