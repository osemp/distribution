package proxy

import (
	"context"
	"fmt"

	"github.com/docker/distribution/registry/client/auth"
	"github.com/docker/distribution/registry/client/auth/challenge"

	"github.com/docker/distribution"
	"github.com/opencontainers/go-digest"
)

type emptyBlobService struct {
}

func (*emptyBlobService) Stat(ctx context.Context, dgst digest.Digest) (distribution.Descriptor, error) {
	return distribution.Descriptor{}, fmt.Errorf("sealer: empty blob stat, err: %s", distribution.ErrUnsupported)
}

func (*emptyBlobService) Get(ctx context.Context, dgst digest.Digest) ([]byte, error) {
	return []byte{}, fmt.Errorf("sealer: empty blob get, err: %s", distribution.ErrUnsupported)
}

func (*emptyBlobService) Open(ctx context.Context, dgst digest.Digest) (distribution.ReadSeekCloser, error) {
	return nil, fmt.Errorf("sealer: empty blob open, err: %s", distribution.ErrUnsupported)
}

func (*emptyBlobService) Put(ctx context.Context, mediaType string, p []byte) (distribution.Descriptor, error) {
	return distribution.Descriptor{}, fmt.Errorf("sealer: empty blob put, err: %s", distribution.ErrUnsupported)
}

func (*emptyBlobService) Create(ctx context.Context, options ...distribution.BlobCreateOption) (distribution.BlobWriter, error) {
	return nil, fmt.Errorf("sealer: empty blob create, err: %s", distribution.ErrUnsupported)
}

func (*emptyBlobService) Resume(ctx context.Context, id string) (distribution.BlobWriter, error) {
	return nil, fmt.Errorf("sealer: empty blob resume, err: %s", distribution.ErrUnsupported)
}

type emptyManifestService struct {
}

func (*emptyManifestService) Exists(ctx context.Context, dgst digest.Digest) (bool, error) {
	return false, fmt.Errorf("sealer empty manifest exists, err: %s", distribution.ErrUnsupported)
}

func (*emptyManifestService) Get(ctx context.Context, dgst digest.Digest, options ...distribution.ManifestServiceOption) (distribution.Manifest, error) {
	return nil, fmt.Errorf("sealer empty manifest get, err: %s", distribution.ErrUnsupported)
}

func (*emptyManifestService) Put(ctx context.Context, manifest distribution.Manifest, options ...distribution.ManifestServiceOption) (digest.Digest, error) {
	return "", fmt.Errorf("sealer empty manifest put, err: %s", distribution.ErrUnsupported)
}

func (*emptyManifestService) Delete(ctx context.Context, dgst digest.Digest) error {
	return fmt.Errorf("sealer empty manifest delete, err: %s", distribution.ErrUnsupported)
}

type emptyChallenger struct {
}

func (*emptyChallenger) tryEstablishChallenges(context.Context) error {
	return fmt.Errorf("sealer tryEstablishChallenges, err: %s", distribution.ErrUnsupported)
}

func (*emptyChallenger) credentialStore() auth.CredentialStore {
	return nil
}

func (*emptyChallenger) challengeManager() challenge.Manager {
	return nil
}

type emptyTagService struct {
}

func (*emptyTagService) Get(ctx context.Context, tag string) (distribution.Descriptor, error) {
	return distribution.Descriptor{}, fmt.Errorf("sealer empty tag get, err: %s", distribution.ErrUnsupported)
}

// Tag associates the tag with the provided descriptor, updating the
// current association, if needed.
func (*emptyTagService) Tag(ctx context.Context, tag string, desc distribution.Descriptor) error {
	return fmt.Errorf("sealer empty tag tag, err: %s", distribution.ErrUnsupported)
}

// Untag removes the given tag association
func (*emptyTagService) Untag(ctx context.Context, tag string) error {
	return fmt.Errorf("sealer empty tag untag, err: %s", distribution.ErrUnsupported)
}

// All returns the set of tags managed by this tag service
func (*emptyTagService) All(ctx context.Context) ([]string, error) {
	return nil, fmt.Errorf("sealer empty tag all, err: %s", distribution.ErrUnsupported)
}

// Lookup returns the set of tags referencing the given digest.
func (*emptyTagService) Lookup(ctx context.Context, digest distribution.Descriptor) ([]string, error) {
	return nil, fmt.Errorf("sealer empty tag lookup, err: %s", distribution.ErrUnsupported)
}
